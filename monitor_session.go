package zelio

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

const (
	monitorStatusRegion    = 0x6B
	monitorInitRegion      = 0x6C
	monitorMomentaryRegion = 0x78
	monitorMomentaryBytes  = 0x04
)

var ErrMonitoringNotReady = errors.New("Zelio Monitoring is not ready")

// EnsureMonitoring requests initialization for the observed FBD RUN status.
// The caller must verify the program and relay before enabling writes.
// The caller must own the logical session for the entire check and write.
// The relay can acknowledge an impulse while Monitoring is inactive.
// The capture shows the init command followed by status 83 00.
func (c *Client) EnsureMonitoring(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	status, err := c.ReadMonitoringStatus(ctx)
	if err != nil {
		return err
	}
	switch status {
	case [2]byte{0x83, 0}:
		return nil
	case [2]byte{0x81, 0}:
		if len(c.options.MomentarySignals) == 0 {
			return ErrReadOnly
		}
	default:
		return fmt.Errorf("%w: unexpected status %02x", ErrMonitoringNotReady, status)
	}

	before, err := c.readRUNBeforeInitialization(ctx)
	if err != nil {
		return err
	}
	if err := c.initializeMonitoring(ctx); err != nil {
		return err
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err = c.ReadMonitoringStatus(ctx)
		if err != nil {
			return err
		}
		if status == [2]byte{0x83, 0} {
			after, err := c.ReadMonitoringIO(ctx)
			if err != nil {
				return fmt.Errorf("read I/O after Monitoring initialization: %w", err)
			}
			if after.Available {
				if after.Mode != "RUN" || !slices.Equal(before.Outputs, after.Outputs) {
					return fmt.Errorf("%w: mode or outputs changed during initialization", ErrMonitoringNotReady)
				}
				return nil
			}
		} else if status != [2]byte{0x81, 0} {
			return fmt.Errorf("%w: status after initialization %02x", ErrMonitoringNotReady, status)
		}
		select {
		case <-ctx.Done():
			return errors.Join(ErrMonitoringNotReady, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (c *Client) readRUNBeforeInitialization(ctx context.Context) (MonitoringIO, error) {
	for i := 0; i < 3; i++ {
		snapshot, err := c.ReadMonitoringIO(ctx)
		if err != nil {
			return MonitoringIO{}, fmt.Errorf("read I/O before Monitoring initialization: %w", err)
		}
		if snapshot.Available && snapshot.Mode == "RUN" {
			return snapshot, nil
		}
		if !snapshot.BlankDisplay {
			break
		}
		if i < 2 {
			select {
			case <-ctx.Done():
				return MonitoringIO{}, ctx.Err()
			case <-time.After(30 * time.Millisecond):
			}
		}
	}
	return MonitoringIO{}, fmt.Errorf("%w: initialization requires a valid RUN display", ErrMonitoringNotReady)
}

// ReadMonitoringStatus reads the two-byte status from the observed FBD relay.
func (c *Client) ReadMonitoringStatus(ctx context.Context) ([2]byte, error) {
	ctx, unlock, err := c.lock(ctx)
	if err != nil {
		return [2]byte{}, err
	}
	defer unlock()
	request, _ := MonitoringReadRequest(monitorStatusRegion, 0, 2)
	reply, _, err := c.exchangeLocked(ctx, request)
	if err != nil {
		return [2]byte{}, fmt.Errorf("read Monitoring status: %w", err)
	}
	status, err := ParseMonitoringReadResponse(reply, 2)
	if err != nil {
		c.dirty = true
		return [2]byte{}, fmt.Errorf("parse Monitoring status: %w", err)
	}
	return [2]byte(status), nil
}

func (c *Client) initializeMonitoring(ctx context.Context) error {
	ctx, unlock, err := c.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	ack := []byte{1, 0x10, 0, 0, monitorInitRegion, 0, 2}
	request := monitoringEncode(append(ack, 0x04, 0))
	reply, sent, err := c.exchangeLocked(ctx, request)
	if err == nil {
		var data []byte
		data, err = monitoringDecode(reply)
		if err == nil && !slices.Equal(data, ack) {
			err = fmt.Errorf("%w: Monitoring initialization acknowledgement", ErrProtocol)
		}
		if err != nil {
			c.dirty = true
		}
	}
	if err != nil && sent {
		err = errors.Join(ErrWriteUncertain, err)
	}
	if err != nil {
		return fmt.Errorf("initialize Monitoring: %w", err)
	}
	return nil
}

// MonitoringMomentaryRequest reproduces one Zelio Soft Monitoring Mode click
// on a FBD link. The captured 0x78 write is a one-shot event: Zelio Soft sends
// no matching release frame. Signal IDs are program-specific; callers must use
// only IDs identified in a capture of the installed program.
func MonitoringMomentaryRequest(signalID uint8) ([]byte, error) {
	if signalID == 0 {
		return nil, errors.New("monitoring signal ID must be nonzero")
	}
	return monitoringEncode([]byte{1, 0x10, 0, 0, monitorMomentaryRegion, 0,
		monitorMomentaryBytes, 0xE0, signalID, 0, 1}), nil
}

func validateMonitoringMomentaryACK(frame []byte) error {
	data, err := monitoringDecode(frame)
	if err != nil {
		return err
	}
	want := []byte{1, 0x10, 0, 0, monitorMomentaryRegion, 0, monitorMomentaryBytes}
	if string(data) != string(want) {
		return fmt.Errorf("%w: monitoring momentary acknowledgement", ErrProtocol)
	}
	return nil
}

// SendMonitoringMomentary sends exactly one event and never retries it. An ACK
// confirms receipt only; callers must read the relevant output afterwards.
func (c *Client) SendMonitoringMomentary(ctx context.Context, signalID uint8) error {
	if len(c.options.MomentarySignals) == 0 {
		return ErrReadOnly
	}
	if !slices.Contains(c.options.MomentarySignals, signalID) {
		return ErrUnverifiedWrite
	}
	request, err := MonitoringMomentaryRequest(signalID)
	if err != nil {
		return err
	}
	ctx, unlock, err := c.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	reply, sent, err := c.exchangeLocked(ctx, request)
	if err == nil {
		err = validateMonitoringMomentaryACK(reply)
		if err != nil {
			c.dirty = true
		}
	}
	if err != nil && sent {
		return errors.Join(ErrWriteUncertain, err)
	}
	return err
}
