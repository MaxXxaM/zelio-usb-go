package bridge

import (
	"context"
	"errors"
	"time"

	zelio "github.com/MaxXxaM/zelio-usb-go"
)

const momentaryReadInterval = 150 * time.Millisecond

var ErrInterlocked = errors.New("a configured physical interlock is active or unreadable")

// MomentaryDevice is implemented by the USB client with momentary signals
// enabled. A caller must own the session exclusively while performing a command.
type MomentaryDevice interface {
	ReadMonitoringIO(context.Context) (zelio.MonitoringIO, error)
	EnsureMonitoring(context.Context) error
	SendMonitoringMomentary(context.Context, uint8) error
}

func (b Binding) interlocked(snapshot zelio.MonitoringIO) bool {
	for _, label := range b.BlockWhen {
		on, ok := snapshot.Lookup(label)
		if !ok || on {
			return true
		}
	}
	return false
}

// ActuateMomentary sets a verified group to the desired logical state. It
// checks the outputs before and after initialization and sends at most one
// impulse. It never retries a write of uncertain outcome.
func ActuateMomentary(ctx context.Context, plc MomentaryDevice, b Binding, want bool) (zelio.MonitoringIO, error) {
	if err := ValidateBindings([]Binding{b}); err != nil {
		return zelio.MonitoringIO{}, err
	}
	if !b.CanMomentary() {
		return zelio.MonitoringIO{}, zelio.ErrUnverifiedWrite
	}
	snapshot, current, err := readMomentaryState(ctx, plc, b)
	if err != nil || current == want {
		return snapshot, err
	}
	if err := plc.EnsureMonitoring(ctx); err != nil {
		return zelio.MonitoringIO{}, err
	}
	snapshot, current, err = readMomentaryState(ctx, plc, b)
	if err != nil || current == want {
		return snapshot, err
	}
	if err := plc.SendMonitoringMomentary(ctx, b.SignalID); err != nil {
		return zelio.MonitoringIO{}, err
	}

	deadline := time.Now().Add(2 * time.Second)
	redrawDeadline := time.Now().Add(lcdBlankGrace)
	for {
		snapshot, err = plc.ReadMonitoringIO(ctx)
		if err != nil {
			return zelio.MonitoringIO{}, errors.Join(ErrUnconfirmed, err)
		}
		if current, available := b.Lookup(snapshot); available && current == want {
			return snapshot, nil
		}
		expiry := deadline
		if snapshot.BlankDisplay {
			expiry = redrawDeadline
		}
		if !time.Now().Before(expiry) {
			return snapshot, ErrUnconfirmed
		}
		if err := wait(ctx, momentaryReadInterval); err != nil {
			return snapshot, errors.Join(ErrUnconfirmed, err)
		}
	}
}

func readMomentaryState(ctx context.Context, plc MomentaryDevice, b Binding) (zelio.MonitoringIO, bool, error) {
	deadline := time.Now().Add(lcdBlankGrace)
	for {
		snapshot, err := plc.ReadMonitoringIO(ctx)
		if err != nil {
			return zelio.MonitoringIO{}, false, err
		}
		if snapshot.BlankDisplay && time.Now().Before(deadline) {
			if err := wait(ctx, momentaryReadInterval); err != nil {
				return snapshot, false, err
			}
			continue
		}
		current, available := b.Lookup(snapshot)
		if !available {
			return snapshot, false, ErrUnavailable
		}
		if b.interlocked(snapshot) {
			return snapshot, false, ErrInterlocked
		}
		return snapshot, current, nil
	}
}
