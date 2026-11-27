package bridge

import (
	"context"
	"errors"
	"testing"

	zelio "github.com/MaxXxaM/zelio-usb-go"
)

type impulsePLC struct {
	fakePLC
	initCalls int
	impulses  []uint8
}

func (p *impulsePLC) EnsureMonitoring(context.Context) error {
	p.initCalls++
	return nil
}

func (p *impulsePLC) SendMonitoringMomentary(_ context.Context, id uint8) error {
	p.impulses = append(p.impulses, id)
	return p.writeErr
}

func groupedIO(a, b, interlock bool) zelio.MonitoringIO {
	return zelio.MonitoringIO{Available: true, Mode: "RUN",
		Inputs:  []zelio.DigitalPoint{{Label: "1", On: interlock}},
		Outputs: []zelio.DigitalPoint{{Label: "A", On: a}, {Label: "B", On: b}},
	}
}

func TestActuateMomentaryUsesVerifiedGroupReadback(t *testing.T) {
	binding := Binding{ID: "tracks", Name: "Tracks", Labels: []string{"QA", "QB"},
		Control: VerifiedMomentary, SignalID: 37, BlockWhen: []string{"I1"}}
	for _, tc := range []struct {
		name              string
		reads             []zelio.MonitoringIO
		initCalls, pulses int
		writeErr, wantErr error
	}{
		{"group ON", []zelio.MonitoringIO{groupedIO(false, false, false), groupedIO(false, false, false), groupedIO(true, true, false)}, 1, 1, nil, nil},
		{"blank LCD redraw", []zelio.MonitoringIO{
			{BlankDisplay: true}, {BlankDisplay: true}, {BlankDisplay: true}, groupedIO(false, false, false),
			groupedIO(false, false, false), groupedIO(true, true, false),
		}, 1, 1, nil, nil},
		{"blank then interlock", []zelio.MonitoringIO{{BlankDisplay: true}, groupedIO(false, false, true)}, 0, 0, nil, ErrInterlocked},
		{"same state", []zelio.MonitoringIO{groupedIO(true, true, false)}, 0, 0, nil, nil},
		{"mismatch", []zelio.MonitoringIO{groupedIO(true, false, false)}, 0, 0, nil, ErrUnavailable},
		{"physical interlock", []zelio.MonitoringIO{groupedIO(false, false, true)}, 0, 0, nil, ErrInterlocked},
		{"already switched during init", []zelio.MonitoringIO{groupedIO(false, false, false), groupedIO(true, true, false)}, 1, 0, nil, nil},
		{"uncertain pulse", []zelio.MonitoringIO{groupedIO(false, false, false), groupedIO(false, false, false)}, 1, 1, zelio.ErrWriteUncertain, zelio.ErrWriteUncertain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plc := &impulsePLC{fakePLC: fakePLC{reads: tc.reads, writeErr: tc.writeErr}}
			_, err := ActuateMomentary(context.Background(), plc, binding, true)
			if !errors.Is(err, tc.wantErr) || plc.initCalls != tc.initCalls || len(plc.impulses) != tc.pulses {
				t.Fatalf("err=%v, init=%d, pulses=%v", err, plc.initCalls, plc.impulses)
			}
			if len(plc.impulses) != 0 && plc.impulses[0] != binding.SignalID {
				t.Fatalf("unexpected impulse %v", plc.impulses)
			}
		})
	}
}
