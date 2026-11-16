package zelio

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Monitoring uses the same ASCII envelope as SL IN/OUT, but different regions.
// These addresses and payloads were observed from Zelio Soft 2 V5.4.2 on an
// SR3B261FU + SR3XT141FU in RUN. They are not documented as a stable public API.
// VerifiedQ1Signal is program-specific and must be confirmed on each installation.
const VerifiedQ1Signal uint8 = 0x2A

const (
	monitorIORegion    = 0x6F
	monitorIOByteCount = 0x4E
	monitorForceRegion = 0x79
	monitorForceBytes  = 0x1E
)

// The captured Zelio Soft monitoring frames use the usual two's-complement
// ASCII LRC, unlike the documented SL IN/OUT frames' 1-sum check byte.
func monitoringChecksum(data []byte) byte {
	var sum byte
	for _, b := range data {
		sum += b
	}
	return -sum
}

func monitoringEncode(data []byte) []byte {
	payload := append(append([]byte(nil), data...), monitoringChecksum(data))
	return []byte(":" + strings.ToUpper(hex.EncodeToString(payload)) + "\r\n")
}

func monitoringDecode(frame []byte) ([]byte, error) {
	if len(frame) < 9 || frame[0] != ':' || string(frame[len(frame)-2:]) != "\r\n" {
		return nil, fmt.Errorf("%w: monitoring framing", ErrProtocol)
	}
	data, err := hex.DecodeString(string(frame[1 : len(frame)-2]))
	if err != nil || len(data) < 3 {
		return nil, fmt.Errorf("%w: monitoring hexadecimal encoding", ErrProtocol)
	}
	if monitoringChecksum(data[:len(data)-1]) != data[len(data)-1] || data[0] != 1 {
		return nil, fmt.Errorf("%w: monitoring checksum/slave", ErrProtocol)
	}
	return data[:len(data)-1], nil
}

// MonitorForceKind describes the three observed write forms. ReleaseAll clears
// all forces on the PLC, including ones created by another operator.
type MonitorForceKind uint8

const (
	MonitorForce MonitorForceKind = iota
	MonitorRelease
	MonitorReleaseAll
)

// MonitoringReadRequest builds a read from a Zelio Soft monitoring region.
// Offset and count are bytes, not SL words or physical I/Q labels.
func MonitoringReadRequest(region, offset, count uint8) ([]byte, error) {
	if count == 0 {
		return nil, errors.New("monitoring read count must be positive")
	}
	return monitoringEncode([]byte{1, 3, 0, 0, region, offset, count}), nil
}

// ParseMonitoringReadResponse verifies framing, checksum, function and length.
func ParseMonitoringReadResponse(frame []byte, count uint8) ([]byte, error) {
	data, err := monitoringDecode(frame)
	if err != nil {
		return nil, err
	}
	if len(data) != 3+int(count) || data[1] != 3 || data[2] != count {
		return nil, fmt.Errorf("%w: monitoring read size/function", ErrProtocol)
	}
	return append([]byte(nil), data[3:]...), nil
}

// MonitoringForceRequest reproduces Zelio Soft's observed 30-byte write to
// region 0x79. The only signal ID identified on the real program is 0x2A,
// corresponding to the tested Q1 FBD line. Other IDs need verification.
// on is the value retained in an individual Release frame.
func MonitoringForceRequest(signalID uint8, kind MonitorForceKind, on bool) ([]byte, error) {
	if kind > MonitorReleaseAll {
		return nil, fmt.Errorf("invalid monitor force kind %d", kind)
	}
	if kind != MonitorReleaseAll && signalID == 0 {
		return nil, errors.New("monitoring signal ID must be nonzero")
	}
	payload := make([]byte, monitorForceBytes)
	if kind != MonitorReleaseAll {
		if kind == MonitorForce {
			payload[0] = 0xE0
		}
		payload[1] = signalID
		if on {
			payload[2] = 1
		}
	}
	data := append([]byte{1, 0x10, 0, 0, monitorForceRegion, 0, monitorForceBytes}, payload...)
	return monitoringEncode(data), nil
}

func validateMonitoringWriteResponse(frame []byte) error {
	data, err := monitoringDecode(frame)
	if err != nil {
		return err
	}
	want := []byte{1, 0x10, 0, 0, monitorForceRegion, 0, monitorForceBytes}
	if string(data) != string(want) {
		return fmt.Errorf("%w: monitoring write acknowledgement", ErrProtocol)
	}
	return nil
}

// DigitalPoint is one I or Q glyph from the Zelio front-panel I/O display.
type DigitalPoint struct {
	Label string `json:"label"`
	On    bool   `json:"on"`
}

// MonitoringIO is a single front-panel display snapshot. Available=false
// means the PLC LCD was showing another page; it must not be treated as OFF.
type MonitoringIO struct {
	Available bool           `json:"available"`
	Mode      string         `json:"mode,omitempty"`
	Inputs    []DigitalPoint `json:"inputs,omitempty"`
	Outputs   []DigitalPoint `json:"outputs,omitempty"`
}

const monitorInputLabels = "123456789ABCDEFGHJKLNPQR"
const monitorOutputLabels = "123456789ABCDEFG"

// ValidMonitoringLabel matches the I/Q positions in the observed LCD layout.
func ValidMonitoringLabel(label string) bool {
	return len(label) == 2 && ((label[0] == 'I' && strings.ContainsRune(monitorInputLabels, rune(label[1]))) ||
		(label[0] == 'Q' && strings.ContainsRune(monitorOutputLabels, rune(label[1]))))
}

// Lookup returns a point only while the observed I/O page shows RUN.
func (m MonitoringIO) Lookup(label string) (on, available bool) {
	if !m.Available || m.Mode != "RUN" || !ValidMonitoringLabel(label) {
		return false, false
	}
	points := m.Outputs
	if label[0] == 'I' {
		points = m.Inputs
	}
	for _, point := range points {
		if point.Label == label[1:] {
			return point.On, true
		}
	}
	return false, false
}

// ParseMonitoringIO decodes the 0x6F/78-byte reply seen in the capture. Zelio
// sets bit 7 on an active I/Q position and replaces its ASCII glyph with a
// display code. Other display pages are marked unavailable.
func ParseMonitoringIO(frame []byte) (MonitoringIO, error) {
	data, err := ParseMonitoringReadResponse(frame, monitorIOByteCount)
	if err != nil {
		return MonitoringIO{}, err
	}
	result := MonitoringIO{}
	for i, label := range []byte(monitorInputLabels) {
		if data[i] != label && data[i]&0x80 == 0 {
			return result, nil
		}
	}
	for i, label := range []byte(monitorOutputLabels) {
		if data[54+i] != label && data[54+i]&0x80 == 0 {
			return result, nil
		}
	}
	result.Available = true
	result.Mode = strings.TrimSpace(string(data[27:32]))
	for i, label := range monitorInputLabels {
		result.Inputs = append(result.Inputs, DigitalPoint{Label: string(label), On: data[i]&0x80 != 0})
	}
	for i, label := range monitorOutputLabels {
		result.Outputs = append(result.Outputs, DigitalPoint{Label: string(label), On: data[54+i]&0x80 != 0})
	}
	return result, nil
}

// ReadMonitoringIO queries the front-panel display once. The caller may poll
// until Available is true. This is read-only and does not enter Debug or STOP.
func (c *Client) ReadMonitoringIO(ctx context.Context) (MonitoringIO, error) {
	request, _ := MonitoringReadRequest(monitorIORegion, 0, monitorIOByteCount)
	ctx, unlock, err := c.lock(ctx)
	if err != nil {
		return MonitoringIO{}, err
	}
	defer unlock()
	reply, _, err := c.exchangeLocked(ctx, request)
	if err != nil {
		return MonitoringIO{}, err
	}
	result, err := ParseMonitoringIO(reply)
	if err != nil {
		c.dirty = true
	}
	return result, err
}

// ReadMonitoringRawFBD returns the 246-byte region 0x70 snapshot. Its format
// is uninterpreted. Its layout depends on the PLC program.
func (c *Client) ReadMonitoringRawFBD(ctx context.Context) ([]byte, error) {
	const count = 0xF6
	request, _ := MonitoringReadRequest(0x70, 0, count)
	ctx, unlock, err := c.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	reply, _, err := c.exchangeLocked(ctx, request)
	if err != nil {
		return nil, err
	}
	result, err := ParseMonitoringReadResponse(reply, count)
	if err != nil {
		c.dirty = true
	}
	return result, err
}

// SendMonitoringForce sends one allowlisted frame. Force holds the requested
// value. A successful ACK confirms frame acceptance. Read the logical output
// separately. A transmission error may leave the force active.
func (c *Client) SendMonitoringForce(ctx context.Context, signalID uint8, kind MonitorForceKind, on bool) error {
	if !c.options.AllowMonitoringWrites {
		return ErrReadOnly
	}
	if signalID != VerifiedQ1Signal || (kind != MonitorForce && (kind != MonitorRelease || !on)) {
		return ErrUnverifiedWrite
	}
	request, err := MonitoringForceRequest(signalID, kind, on)
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
		err = validateMonitoringWriteResponse(reply)
		if err != nil {
			c.dirty = true
		}
	}
	if err != nil && sent {
		return errors.Join(ErrWriteUncertain, err)
	}
	return err
}
