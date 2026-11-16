package sr2usb

import (
	"errors"
	"testing"
)

type controlCall struct {
	direction, request uint8
	value, index       uint16
	length             int
}
type fakeControl struct {
	calls []controlCall
	fail  bool
	short bool
}

func (f *fakeControl) Control(direction, request uint8, value, index uint16, data []byte) (int, error) {
	f.calls = append(f.calls, controlCall{direction, request, value, index, len(data)})
	if f.fail {
		return 0, errors.New("USB disconnected")
	}
	if direction == 0xc0 {
		switch request {
		case 5:
			copy(data, []byte{1, 0x60})
		case 10:
			data[0] = 16
		}
		if f.short {
			return 0, nil
		}
		return len(data), nil
	}
	return 0, nil
}
func TestProbeUsesOnlyINControlTransfers(t *testing.T) {
	f := &fakeControl{}
	s, err := probeControl(f)
	if err != nil || s.ModemStatus != "0160" || s.LatencyMS != 16 {
		t.Fatalf("%+v %v", s, err)
	}
	if len(f.calls) != 2 {
		t.Fatal(f.calls)
	}
	for _, call := range f.calls {
		if call.direction != 0xc0 {
			t.Fatal("mutating probe", call)
		}
	}
}
func TestProbeRejectsTruncatedReply(t *testing.T) {
	if _, err := probeControl(&fakeControl{short: true}); err == nil {
		t.Fatal("accepted short reply")
	}
}
func TestUARTConfiguration1152007E1(t *testing.T) {
	f := &fakeControl{}
	if err := configureUART(f, UARTOptions{DTR: true, RTS: true}); err != nil {
		t.Fatal(err)
	}
	want := []controlCall{
		{0x40, 0, 0, 1, 0},                     // reset UART, not the PLC
		{0x40, 11, 0, 1, 0},                    // disable bit-bang mode
		{0x40, 3, 26, 0, 0},                    // 3 MHz / 26 = 115384.6 baud (FT232R divisor encoding)
		{0x40, 4, 0x0207, 1, 0},                // 7 bits, even parity, one stop bit
		{0x40, 2, 0, 1, 0},                     // no hardware/software flow control
		{0x40, 9, 2, 1, 0},                     // 2 ms latency
		{0x40, 1, 0x0303, 1, 0},                // DTR and RTS asserted
		{0x40, 0, 1, 1, 0}, {0x40, 0, 2, 1, 0}, // purge both UART buffers
	}
	if len(f.calls) != len(want) {
		t.Fatalf("%+v", f.calls)
	}
	for i := range want {
		if f.calls[i] != want[i] {
			t.Fatalf("step %d: %+v want %+v", i, f.calls[i], want[i])
		}
	}
}
func TestUARTConfigurationStopsOnFailure(t *testing.T) {
	f := &fakeControl{fail: true}
	if err := configureUART(f, UARTOptions{}); err == nil || len(f.calls) != 1 {
		t.Fatalf("%+v %v", f.calls, err)
	}
}
