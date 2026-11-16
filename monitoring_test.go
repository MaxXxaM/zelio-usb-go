package zelio

import (
	"context"
	"errors"
	"testing"
)

// Write vectors reproduce the observed command bytes without device data.
const (
	observedForceOff   = ":0110000079001EE02A000000000000000000000000000000000000000000000000000000004E\r\n"
	observedForceOn    = ":0110000079001EE02A010000000000000000000000000000000000000000000000000000004D\r\n"
	observedRelease    = ":0110000079001E002A010000000000000000000000000000000000000000000000000000002D\r\n"
	observedReleaseAll = ":0110000079001E00000000000000000000000000000000000000000000000000000000000058\r\n"
)

func TestObservedMonitoringForceFrames(t *testing.T) {
	for _, tc := range []struct {
		kind MonitorForceKind
		on   bool
		want string
	}{
		{MonitorForce, false, observedForceOff},
		{MonitorForce, true, observedForceOn},
		{MonitorRelease, true, observedRelease},
		{MonitorReleaseAll, false, observedReleaseAll},
	} {
		got, err := MonitoringForceRequest(0x2A, tc.kind, tc.on)
		if err != nil || string(got) != tc.want {
			t.Fatalf("kind=%v on=%v: %q %v", tc.kind, tc.on, got, err)
		}
	}
}

func syntheticIO(on bool) []byte {
	data := make([]byte, 78)
	for i := range data {
		data[i] = ' '
	}
	copy(data, "123456789ABCDEFGHJKLNPQR")
	copy(data[27:], "RUN")
	copy(data[54:], "123456789ABCDEFG")
	if on {
		data[54] = 0x81
	}
	return monitoringEncode(append([]byte{1, 3, 78}, data...))
}

func TestMonitoringIOResponse(t *testing.T) {
	off, err := ParseMonitoringIO(syntheticIO(false))
	if err != nil {
		t.Fatal(err)
	}
	on, err := ParseMonitoringIO(syntheticIO(true))
	if err != nil {
		t.Fatal(err)
	}
	if !off.Available || !on.Available || off.Mode != "RUN" || on.Mode != "RUN" {
		t.Fatalf("off=%+v on=%+v", off, on)
	}
	if len(off.Inputs) != 24 || len(off.Outputs) != 16 || len(on.Outputs) != 16 {
		t.Fatalf("unexpected I/O counts: %+v", off)
	}
	for i := range off.Outputs {
		if off.Outputs[i].Label != on.Outputs[i].Label {
			t.Fatalf("output label mismatch at %d", i)
		}
		if off.Outputs[i].On != on.Outputs[i].On && i != 0 {
			t.Fatalf("unexpected changed output %s", off.Outputs[i].Label)
		}
	}
	if off.Outputs[0].On || !on.Outputs[0].On {
		t.Fatal("synthetic physical OUT 1 transition not decoded")
	}
}

func TestMonitoringDisplayBlankIsUnknown(t *testing.T) {
	data := make([]byte, 78)
	for i := range data {
		data[i] = ' '
	}
	frame := monitoringEncode(append([]byte{1, 3, 78}, data...))
	io, err := ParseMonitoringIO(frame)
	if err != nil || io.Available || len(io.Outputs) != 0 {
		t.Fatalf("blank display must not mean all lights off: %+v %v", io, err)
	}
}

func TestMonitoringClientReadAndWriteGuards(t *testing.T) {
	w := &fakeWire{replies: [][]byte{syntheticIO(true), []byte(":0110000079001E58\r\n")}}
	c := newTestClient(t, w, true)
	io, err := c.ReadMonitoringIO(context.Background())
	if err != nil || !io.Available || !io.Outputs[0].On {
		t.Fatalf("read: %+v %v", io, err)
	}
	if string(w.writes[0]) != ":010300006F004E3F\r\n" {
		t.Fatalf("read request %q", w.writes[0])
	}
	if err := c.SendMonitoringForce(context.Background(), 0x2A, MonitorForce, false); err != nil {
		t.Fatal(err)
	}
	if string(w.writes[1]) != observedForceOff {
		t.Fatalf("force request %q", w.writes[1])
	}
	ro := newTestClient(t, &fakeWire{}, false)
	if err := ro.SendMonitoringForce(context.Background(), 0x2A, MonitorForce, true); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("write guard: %v", err)
	}
}

func TestMonitoringRawFBDLongReply(t *testing.T) {
	data := make([]byte, 0xF6)
	data[5] = 0x64 // captured ON sample: this byte became 0x60 after OFF
	w := &fakeWire{replies: [][]byte{monitoringEncode(append([]byte{1, 3, 0xF6}, data...))}, chunk: 31}
	c := newTestClient(t, w, false)
	got, err := c.ReadMonitoringRawFBD(context.Background())
	if err != nil || len(got) != 0xF6 || got[5] != 0x64 {
		t.Fatalf("raw FBD: len=%d err=%v", len(got), err)
	}
	if string(w.writes[0]) != ":010300007000F696\r\n" {
		t.Fatalf("raw FBD request %q", w.writes[0])
	}
}

func TestMonitoringRefusesUnverifiedLiveWrites(t *testing.T) {
	for _, tc := range []struct {
		id   uint8
		kind MonitorForceKind
		on   bool
	}{
		{0x2b, MonitorForce, true},
		{0x2a, MonitorRelease, false},
		{0, MonitorReleaseAll, false},
	} {
		w := &fakeWire{}
		c := newTestClient(t, w, true)
		if err := c.SendMonitoringForce(context.Background(), tc.id, tc.kind, tc.on); !errors.Is(err, ErrUnverifiedWrite) {
			t.Fatalf("id 0x%02x, kind %v: %v", tc.id, tc.kind, err)
		}
		if len(w.writes) != 0 {
			t.Fatal("unverified frame sent")
		}
	}
}

func TestMonitoringWriteTimeoutPoisonsSession(t *testing.T) {
	w := &fakeWire{}
	c := newTestClient(t, w, true)
	err := c.SendMonitoringForce(context.Background(), 0x2a, MonitorForce, true)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrWriteUncertain) {
		t.Fatal(err)
	}
	if _, err := c.ReadMonitoringIO(context.Background()); !errors.Is(err, ErrDesynchronized) {
		t.Fatal(err)
	}
	if len(w.writes) != 1 {
		t.Fatalf("writes: %d", len(w.writes))
	}
}
