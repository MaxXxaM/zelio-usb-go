package zelio

import (
	"context"
	"errors"
	"testing"
	"time"
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
	if err != nil || io.Available || !io.BlankDisplay || len(io.Outputs) != 0 {
		t.Fatalf("blank display must not mean all lights off: %+v %v", io, err)
	}
}

func TestMonitoringStopWithEmptyIOIsNotBlank(t *testing.T) {
	data := make([]byte, 78)
	for i := range data {
		data[i] = ' '
	}
	copy(data[27:], "STOP")
	frame := monitoringEncode(append([]byte{1, 3, 78}, data...))
	state, err := ParseMonitoringIO(frame)
	if err != nil || state.Available || state.BlankDisplay {
		t.Fatalf("STOP with empty I/O must be reported immediately: %+v %v", state, err)
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

func TestEnsureMonitoringStartsOnlyFromObservedInactiveRUN(t *testing.T) {
	const (
		statusQuery = ":010300006B00028F\r\n"
		inactive    = ":010302810079\r\n"
		active      = ":010302830077\r\n"
		ioQuery     = ":010300006F004E3F\r\n"
		initRequest = ":011000006C000204007D\r\n"
		initAck     = ":011000006C000281\r\n"
	)
	for _, tc := range []struct {
		name     string
		replies  []string
		writes   []string
		canWrite bool
		wantErr  error
	}{
		{"already active", []string{active}, []string{statusQuery}, true, nil},
		{"cold start", []string{inactive, string(syntheticIO(false)), initAck, active, string(syntheticIO(false))},
			[]string{statusQuery, ioQuery, initRequest, statusQuery, ioQuery}, true, nil},
		{"one blank before initialization",
			[]string{inactive, blankLCD(), string(syntheticIO(false)), initAck, active, string(syntheticIO(false))},
			[]string{statusQuery, ioQuery, ioQuery, initRequest, statusQuery, ioQuery}, true, nil},
		{"read only", []string{inactive}, []string{statusQuery}, false, ErrReadOnly},
		{"unknown mode", []string{":01030280007A\r\n"}, []string{statusQuery}, true, ErrMonitoringNotReady},
		{"no run display", []string{inactive, string(monitoringEncode(append([]byte{1, 3, 78}, make([]byte, 78)...)))},
			[]string{statusQuery, ioQuery}, true, ErrMonitoringNotReady},
		{"missing acknowledgement", []string{inactive, string(syntheticIO(false))},
			[]string{statusQuery, ioQuery, initRequest}, true, ErrWriteUncertain},
		{"output changed", []string{inactive, string(syntheticIO(false)), initAck, active, string(syntheticIO(true))},
			[]string{statusQuery, ioQuery, initRequest, statusQuery, ioQuery}, true, ErrMonitoringNotReady},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &fakeWire{}
			for _, frame := range tc.replies {
				w.replies = append(w.replies, []byte(frame))
			}
			options := Options{Timeout: 30 * time.Millisecond, MomentarySignals: []uint8{0x22}}
			if !tc.canWrite {
				options.MomentarySignals = nil
			}
			c, err := NewClient(w, options)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.EnsureMonitoring(context.Background()); !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
			if len(w.writes) != len(tc.writes) {
				t.Fatalf("sent %q, want %q", w.writes, tc.writes)
			}
			for i, want := range tc.writes {
				if string(w.writes[i]) != want {
					t.Fatalf("request %d: sent %q, want %q", i, w.writes[i], want)
				}
			}
		})
	}
}

func TestMomentaryAllowlistAndNoRetry(t *testing.T) {
	w := &fakeWire{}
	ids := []uint8{0x22}
	c, err := NewClient(w, Options{Timeout: 30 * time.Millisecond, MomentarySignals: ids})
	if err != nil {
		t.Fatal(err)
	}
	ids[0] = 0x23
	if err := c.SendMonitoringMomentary(context.Background(), 0x23); !errors.Is(err, ErrUnverifiedWrite) || len(w.writes) != 0 {
		t.Fatalf("out-of-map write: %v; frames %q", err, w.writes)
	}
	if err := c.SendMonitoringMomentary(context.Background(), 0x22); !errors.Is(err, ErrWriteUncertain) {
		t.Fatalf("expected uncertainty: %v", err)
	}
	if len(w.writes) != 1 || string(w.writes[0]) != ":01100000780004E022000170\r\n" {
		t.Fatalf("unexpected command frames: %q", w.writes)
	}
}

func blankLCD() string {
	data := make([]byte, 78)
	for i := range data {
		data[i] = ' '
	}
	return string(monitoringEncode(append([]byte{1, 3, 78}, data...)))
}
