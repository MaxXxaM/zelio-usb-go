// Package bridge maps PLC observations and commands to application channels.
// MQTT and USB adapters sit outside this package.
package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	zelio "github.com/MaxXxaM/zelio-usb-go"
)

const (
	VerifiedQ1        = "verified-q1-force-maintain"
	VerifiedMomentary = "verified-momentary"
)

var (
	ErrUnavailable = errors.New("output state unavailable on the RUN I/O page")
	ErrUnconfirmed = errors.New("output readback did not confirm the requested state")
	tokenPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)
)

// Binding gives a front-panel label a stable application identifier and name.
// The optional Control value asserts verification on the exact PLC program.
type Binding struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Label     string   `json:"label,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	Control   string   `json:"control,omitempty"`
	SignalID  uint8    `json:"signal_id,omitempty"`
	BlockWhen []string `json:"block_when,omitempty"`
}

func (b Binding) CanForce() bool     { return b.Control == VerifiedQ1 && b.Label == "Q1" }
func (b Binding) CanMomentary() bool { return b.Control == VerifiedMomentary && b.SignalID != 0 }
func (b Binding) CanControl() bool   { return b.CanForce() || b.CanMomentary() }

func (b Binding) outputLabels() []string {
	if len(b.Labels) > 0 {
		return b.Labels
	}
	return []string{b.Label}
}

// Lookup returns a group's state only when all its members agree in RUN.
func (b Binding) Lookup(snapshot zelio.MonitoringIO) (on, available bool) {
	for i, label := range b.outputLabels() {
		value, ok := snapshot.Lookup(label)
		if !ok || (i > 0 && value != on) {
			return false, false
		}
		on = value
	}
	return on, true
}

func ValidID(id string) bool { return tokenPattern.MatchString(id) }

func ValidateBindings(bindings []Binding) error {
	if len(bindings) == 0 {
		return errors.New("at least one channel is required")
	}
	ids, labels := map[string]bool{}, map[string]bool{}
	signals := map[uint8]bool{}
	for _, b := range bindings {
		if !ValidID(b.ID) || strings.TrimSpace(b.Name) == "" {
			return fmt.Errorf("invalid channel id/name for %q", b.ID)
		}
		if ids[b.ID] || (b.Label != "" && len(b.Labels) > 0) {
			return fmt.Errorf("channel %q: duplicate ID or both label and labels provided", b.ID)
		}
		if b.Control != "" && !b.CanControl() {
			return fmt.Errorf("channel %q: invalid control or missing verified signal ID", b.ID)
		}
		if !b.CanMomentary() && (len(b.Labels) > 0 || b.SignalID != 0 || len(b.BlockWhen) > 0) {
			return fmt.Errorf("channel %q: labels, signal_id and block_when require %q", b.ID, VerifiedMomentary)
		}
		for _, label := range b.outputLabels() {
			if !zelio.ValidMonitoringLabel(label) || (b.CanMomentary() && label[0] != 'Q') || labels[label] {
				return fmt.Errorf("channel %q: invalid or duplicate label %q", b.ID, label)
			}
			labels[label] = true
		}
		inputs := map[string]bool{}
		for _, label := range b.BlockWhen {
			if !zelio.ValidMonitoringLabel(label) || label[0] != 'I' || inputs[label] {
				return fmt.Errorf("channel %q: invalid or duplicate interlock input %q", b.ID, label)
			}
			inputs[label] = true
		}
		if b.CanMomentary() {
			if signals[b.SignalID] {
				return fmt.Errorf("channel %q: signal %d already belongs to another group", b.ID, b.SignalID)
			}
			signals[b.SignalID] = true
		}
		ids[b.ID] = true
	}
	return nil
}

type Device interface {
	ReadMonitoringIO(context.Context) (zelio.MonitoringIO, error)
	SendMonitoringForce(context.Context, uint8, zelio.MonitorForceKind, bool) error
	Close() error
}

type State struct {
	ID           string
	Available    bool
	On           bool
	ControlReady bool
}

type Command struct {
	ID string
	On bool
}

type Settings struct {
	Bindings     []Binding
	PollInterval time.Duration
	RetryDelay   time.Duration
	// ControlArmed authorizes control on the first uninterrupted PLC session.
	ControlArmed bool
}

type Open func(context.Context) (Device, error)
type Publish func(context.Context, []State) error

// ActuateQ1 checks fresh RUN state, sends at most one write and reads back Q1.
// A caller must first verify Q1's program mapping and own the session exclusively.
// Individual release is supported only for a currently ON Q1, using the verified
// Release ON frame. A same-state force request performs no write.
func ActuateQ1(ctx context.Context, plc Device, kind zelio.MonitorForceKind, on bool) (zelio.MonitoringIO, error) {
	if kind != zelio.MonitorForce && (kind != zelio.MonitorRelease || !on) {
		return zelio.MonitoringIO{}, zelio.ErrUnverifiedWrite
	}
	state, err := plc.ReadMonitoringIO(ctx)
	if err != nil {
		return zelio.MonitoringIO{}, err
	}
	current, valid := state.Lookup("Q1")
	if !valid {
		return state, ErrUnavailable
	}
	if kind == zelio.MonitorRelease && !current {
		return state, fmt.Errorf("%w: release requires Q1 ON", ErrUnavailable)
	}
	if kind == zelio.MonitorForce && current == on {
		return state, nil
	}
	if err := ctx.Err(); err != nil {
		return state, err
	}
	if err := plc.SendMonitoringForce(ctx, zelio.VerifiedQ1Signal, kind, on); err != nil {
		return zelio.MonitoringIO{}, err
	}
	// Allow the program to run before reading the logical output.
	if err := wait(ctx, 150*time.Millisecond); err != nil {
		return zelio.MonitoringIO{}, errors.Join(ErrUnconfirmed, err)
	}
	state, err = plc.ReadMonitoringIO(ctx)
	if err != nil {
		return zelio.MonitoringIO{}, errors.Join(ErrUnconfirmed, err)
	}
	current, valid = state.Lookup("Q1")
	if !valid || current != on {
		return state, ErrUnconfirmed
	}
	return state, nil
}

// Run polls and, when a baseline exists, accepts commands for explicitly
// writable bindings. After a transport error it closes the device, reports
// unavailability, and opens a read-only session. It never resubmits a write.
// Producers should use a nonblocking send on an unbuffered commands channel,
// so commands have no lifetime beyond the session that accepts them.
func Run(ctx context.Context, cfg Settings, open Open, commands <-chan Command, publish Publish, log *slog.Logger) error {
	if err := ValidateBindings(cfg.Bindings); err != nil {
		return err
	}
	if cfg.PollInterval <= 0 || cfg.RetryDelay <= 0 || open == nil || publish == nil {
		return errors.New("bridge requires positive intervals, open and publish callbacks")
	}
	if log == nil {
		log = slog.Default()
	}
	armed := cfg.ControlArmed
	writable := make(map[string]Binding)
	for _, b := range cfg.Bindings {
		if b.CanControl() {
			writable[b.ID] = b
		}
	}
	report := func(ctx context.Context, snapshot zelio.MonitoringIO) error {
		values := make([]State, 0, len(cfg.Bindings))
		for _, b := range cfg.Bindings {
			on, ok := b.Lookup(snapshot)
			values = append(values, State{ID: b.ID, Available: ok, On: on, ControlReady: armed && b.CanControl() && ok && !b.interlocked(snapshot)})
		}
		return publish(ctx, values)
	}
	// Back off after losing the serial session. Frames do not carry transaction
	// IDs and reopening the USB adapter purges the UART before the next read.
	for ctx.Err() == nil {
		plc, deviceErr := open(ctx)
		if deviceErr == nil {
			var publishErr error
			deviceErr, publishErr = poll(ctx, plc, cfg.PollInterval, commands, writable, armed, report, log)
			if err := plc.Close(); err != nil {
				log.Warn("closing PLC session", "error", err)
			}
			if ctx.Err() != nil {
				return nil
			}
			if publishErr != nil {
				return publishErr
			}
		}
		if ctx.Err() != nil {
			break
		}
		log.Warn("PLC unavailable", "error", deviceErr)
		if armed {
			armed = false
			log.Warn("PLC control disarmed; verify the PLC connection before restarting")
		}
		if err := report(ctx, zelio.MonitoringIO{}); err != nil {
			return err
		}
		if err := wait(ctx, cfg.RetryDelay); err != nil {
			break
		}
	}
	return nil
}

func poll(ctx context.Context, plc Device, interval time.Duration, commands <-chan Command, writable map[string]Binding, armed bool, report func(context.Context, zelio.MonitoringIO) error, log *slog.Logger) (deviceErr, publishErr error) {
	// The RUN I/O LCD returns an all-space frame during its periodic redraw.
	// At normal polling rates, ignore up to two known redraw frames.
	// At slower polling rates and on a different page, report immediately.
	blankReads := 0
	reportStable := func(snapshot zelio.MonitoringIO) error {
		if !snapshot.BlankDisplay || interval > 2*time.Second {
			blankReads = 0
			return report(ctx, snapshot)
		}
		blankReads++
		if blankReads >= 3 {
			return report(ctx, snapshot)
		}
		return nil
	}
	snapshot, err := plc.ReadMonitoringIO(ctx)
	if err != nil {
		return err, nil
	}
	if err := reportStable(snapshot); err != nil {
		return nil, err
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return nil, nil
		case <-tick.C:
			snapshot, err = plc.ReadMonitoringIO(ctx)
			if err != nil {
				return err, nil
			}
		case cmd, ok := <-commands:
			if !ok {
				commands = nil
				continue
			}
			binding, allowed := writable[cmd.ID]
			if !armed || !allowed {
				continue
			}
			if binding.CanMomentary() {
				device, ok := plc.(MomentaryDevice)
				if !ok {
					return errors.New("PLC adapter lacks momentary control"), nil
				}
				snapshot, err = ActuateMomentary(ctx, device, binding, cmd.On)
			} else {
				snapshot, err = ActuateQ1(ctx, plc, zelio.MonitorForce, cmd.On)
			}
			if err != nil {
				log.Warn("PLC command was not confirmed", "channel", cmd.ID, "error", err)
				// These errors by themselves describe a valid display
				// read. Joined errors indicate an untrustworthy session.
				if err != ErrUnavailable && err != ErrUnconfirmed && err != ErrInterlocked {
					return err, nil
				}
			}
		}
		if err := reportStable(snapshot); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}
