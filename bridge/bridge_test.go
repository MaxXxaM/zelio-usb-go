package bridge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	zelio "github.com/MaxXxaM/zelio-usb-go"
)

func runIO(on bool) zelio.MonitoringIO {
	return zelio.MonitoringIO{
		Available: true, Mode: "RUN",
		Inputs:  []zelio.DigitalPoint{{Label: "1", On: true}},
		Outputs: []zelio.DigitalPoint{{Label: "1", On: on}},
	}
}

type fakePLC struct {
	reads     []zelio.MonitoringIO
	readErr   error
	writeErr  error
	events    []string
	afterRead func(int)
}

func (p *fakePLC) ReadMonitoringIO(context.Context) (zelio.MonitoringIO, error) {
	p.events = append(p.events, "read")
	defer func() {
		if p.afterRead != nil {
			p.afterRead(len(p.events))
		}
	}()
	if len(p.reads) == 0 {
		return zelio.MonitoringIO{}, p.readErr
	}
	state := p.reads[0]
	p.reads = p.reads[1:]
	return state, nil
}

func TestPollKeepsStateAcrossShortBlankLCDRedraw(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &fakePLC{reads: []zelio.MonitoringIO{runIO(false), {BlankDisplay: true}, {BlankDisplay: true}, {BlankDisplay: true}, runIO(false)}}
	p.afterRead = func(n int) {
		if n == 5 {
			cancel()
		}
	}
	var got []bool
	report := func(_ context.Context, io zelio.MonitoringIO) error {
		got = append(got, io.Available)
		return nil
	}
	deviceErr, publishErr := poll(ctx, p, time.Millisecond, nil, nil, false, report, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if deviceErr != nil || publishErr != nil || !reflect.DeepEqual(got, []bool{true, true}) {
		t.Fatalf("reports %v, device error %v, publish error %v", got, deviceErr, publishErr)
	}
}

func TestDoesNotHoldBlankLCDAfterGrace(t *testing.T) {
	blank := zelio.MonitoringIO{BlankDisplay: true}
	if holdBlankLCD(blank, time.Second, lcdBlankGrace) {
		t.Fatal("blank LCD exceeded the grace period")
	}
	if holdBlankLCD(blank, 3*time.Second, time.Second) {
		t.Fatal("slow poll must report unavailable immediately")
	}
}

func TestPollReportsNonIOPageImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &fakePLC{reads: []zelio.MonitoringIO{runIO(false), {}}}
	p.afterRead = func(n int) {
		if n == 2 {
			cancel()
		}
	}
	var got []bool
	report := func(_ context.Context, io zelio.MonitoringIO) error {
		got = append(got, io.Available)
		return nil
	}
	deviceErr, publishErr := poll(ctx, p, time.Millisecond, nil, nil, false, report, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if deviceErr != nil || publishErr != nil || !reflect.DeepEqual(got, []bool{true, false}) {
		t.Fatalf("reports %v, device error %v, publish error %v", got, deviceErr, publishErr)
	}
}
func (p *fakePLC) SendMonitoringForce(_ context.Context, id uint8, kind zelio.MonitorForceKind, on bool) error {
	if id != 0x2a || kind != zelio.MonitorForce || !on {
		return errors.New("unexpected write")
	}
	p.events = append(p.events, "force ON")
	return p.writeErr
}
func (p *fakePLC) Close() error {
	p.events = append(p.events, "close")
	return nil
}

func TestActuateReadsBeforeAndAfterForce(t *testing.T) {
	p := &fakePLC{reads: []zelio.MonitoringIO{runIO(false), runIO(true)}}
	got, err := ActuateQ1(context.Background(), p, zelio.MonitorForce, true)
	if err != nil || !got.Outputs[0].On || !reflect.DeepEqual(p.events, []string{"read", "force ON", "read"}) {
		t.Fatalf("state %+v error %v events %v", got, err, p.events)
	}
}

func TestActuateDoesNotTrustACK(t *testing.T) {
	p := &fakePLC{reads: []zelio.MonitoringIO{runIO(false), runIO(false)}}
	got, err := ActuateQ1(context.Background(), p, zelio.MonitorForce, true)
	if !errors.Is(err, ErrUnconfirmed) || got.Outputs[0].On {
		t.Fatalf("state %+v error %v", got, err)
	}
}

func TestActuateNoWriteWithoutRUNStateOrWhenAlreadyOn(t *testing.T) {
	for _, tc := range []struct {
		name string
		io   zelio.MonitoringIO
		want error
	}{
		{"missing", zelio.MonitoringIO{}, ErrUnavailable},
		{"stopped", zelio.MonitoringIO{Available: true, Mode: "STOP", Outputs: runIO(false).Outputs}, ErrUnavailable},
		{"already on", runIO(true), nil},
		{"no Q1", zelio.MonitoringIO{Available: true, Mode: "RUN"}, ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakePLC{reads: []zelio.MonitoringIO{tc.io}}
			_, err := ActuateQ1(context.Background(), p, zelio.MonitorForce, true)
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(p.events, []string{"read"}) {
				t.Fatalf("error %v events %v", err, p.events)
			}
		})
	}
}

func TestRunReconnectsWithoutReplayingWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bad := &fakePLC{
		reads:    []zelio.MonitoringIO{runIO(false), runIO(false)},
		writeErr: zelio.ErrWriteUncertain,
	}
	good := &fakePLC{reads: []zelio.MonitoringIO{runIO(false)}}
	calls := 0
	open := func(context.Context) (Device, error) {
		calls++
		if calls == 1 {
			return bad, nil
		}
		return good, nil
	}
	cmds := make(chan Command)
	var states []State
	view := func(_ context.Context, values []State) error {
		states = append(states, values[0])
		if len(states) == 1 {
			go func() {
				select {
				case cmds <- Command{ID: "output", On: true}:
				case <-ctx.Done():
				}
			}()
		}
		if len(states) == 3 {
			go func() {
				select {
				case cmds <- Command{ID: "output", On: true}:
				case <-ctx.Done():
				}
				cancel()
			}()
		}
		return nil
	}
	cfg := Settings{
		Bindings:     []Binding{{ID: "output", Name: "Output", Label: "Q1", Control: VerifiedQ1}},
		PollInterval: time.Hour, RetryDelay: time.Millisecond, ControlArmed: true,
	}
	if err := Run(ctx, cfg, open, cmds, view, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !reflect.DeepEqual(bad.events, []string{"read", "read", "force ON", "close"}) ||
		!reflect.DeepEqual(good.events, []string{"read", "close"}) {
		t.Fatalf("opens %d, first %v, second %v", calls, bad.events, good.events)
	}
	if want := []State{{ID: "output", Available: true, ControlReady: true}, {ID: "output"}, {ID: "output", Available: true}}; !reflect.DeepEqual(states, want) {
		t.Fatalf("states %+v", states)
	}
}

func TestBindingsRejectUnverifiedAndDuplicateChannels(t *testing.T) {
	a := Binding{ID: "a", Name: "Example", Label: "Q1"}
	b := Binding{ID: "b", Name: "Example", Label: "I1"}
	for _, bindings := range [][]Binding{
		{{ID: "bad/+", Name: "Example", Label: "Q1"}},
		{{ID: "a", Name: "Example", Label: "Q2", Control: VerifiedQ1}},
		{{ID: "a", Name: "Example", Label: "I1", Control: VerifiedQ1}},
		{{ID: "a", Name: "Example", Label: "Q1", Control: "0x2a"}},
		{{ID: "a", Name: "Example", Label: "I0"}},
		{a, a},
		{a, {ID: "b", Name: "Example 2", Label: "Q1"}},
	} {
		if err := ValidateBindings(bindings); err == nil {
			t.Errorf("accepted %+v", bindings)
		}
	}
	if err := ValidateBindings([]Binding{a, b}); err != nil {
		t.Fatal(err)
	}
}
