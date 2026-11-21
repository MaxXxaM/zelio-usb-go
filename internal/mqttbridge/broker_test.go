package mqttbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	zelio "github.com/MaxXxaM/zelio-usb-go"
	"github.com/MaxXxaM/zelio-usb-go/bridge"
	"github.com/MaxXxaM/zelio-usb-go/internal/config"
	"github.com/eclipse/paho.golang/paho"
)

type simulatedPLC struct {
	on          atomic.Bool
	blank       atomic.Bool
	writes      chan bool
	initialized atomic.Int32
}

func (p *simulatedPLC) ReadMonitoringIO(context.Context) (zelio.MonitoringIO, error) {
	if p.blank.Load() {
		return zelio.MonitoringIO{BlankDisplay: true}, nil
	}
	return zelio.MonitoringIO{
		Available: true, Mode: "RUN",
		Outputs: []zelio.DigitalPoint{{Label: "1", On: p.on.Load()}, {Label: "A", On: p.on.Load()}, {Label: "B", On: p.on.Load()}},
	}, nil
}
func (p *simulatedPLC) SendMonitoringForce(_ context.Context, _ uint8, _ zelio.MonitorForceKind, value bool) error {
	p.on.Store(value)
	p.writes <- value
	return nil
}
func (p *simulatedPLC) Close() error { return nil }
func (p *simulatedPLC) EnsureMonitoring(context.Context) error {
	p.initialized.Add(1)
	return nil
}
func (p *simulatedPLC) SendMonitoringMomentary(_ context.Context, id uint8) error {
	if id != 37 || p.initialized.Load() == 0 {
		return fmt.Errorf("unexpected pulse %d without initialized Monitoring", id)
	}
	on := !p.on.Load()
	p.on.Store(on)
	p.writes <- on
	return nil
}

// TestBridgeReconnectAndRetainedCommands requires a disposable MQTT 5 broker.
// It exercises MQTT traffic and the real bridge loop with a simulated PLC.
func TestBridgeReconnectAndRetainedCommands(t *testing.T) {
	t.Run("legacy single output", func(t *testing.T) {
		testBridgeReconnect(t, `{"id":"output","name":"Output","label":"Q1","control":"verified-q1-force-maintain"}`)
	})
	t.Run("verified momentary group", func(t *testing.T) {
		testBridgeReconnect(t, `{"id":"output","name":"Output","labels":["QA","QB"],"control":"verified-momentary","signal_id":37}`)
	})
}

func testBridgeReconnect(t *testing.T, binding string) {
	t.Helper()

	addr := os.Getenv("ZELIO_TEST_BROKER")
	if addr == "" {
		t.Skip("set ZELIO_TEST_BROKER to the host:port of a disposable MQTT 5 broker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	id := fmt.Sprintf("test-%x", time.Now().UnixNano())
	cfg, err := config.Decode(strings.NewReader(fmt.Sprintf(`{
	    "device_id": %q, "device_name": "MQTT integration test", "cable_serial": "TEST-CABLE",
	    "mqtt": {"url": %q},
	    "channels": [%s]
	}`, id, "mqtt://"+addr, binding)))
	if err != nil {
		t.Fatal(err)
	}
	observed := make(chan *paho.Publish, 64)
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := paho.NewClient(paho.ClientConfig{
		Conn: conn,
		OnPublishReceived: []func(paho.PublishReceived) (bool, error){
			func(msg paho.PublishReceived) (bool, error) {
				select {
				case observed <- msg.Packet:
				case <-ctx.Done():
				}
				return true, nil
			},
		},
	})
	if _, err := client.Connect(ctx, &paho.Connect{ClientID: id + "-observer", CleanStart: true}); err != nil {
		t.Fatal(err)
	}
	controlTopic := configTopic("homeassistant", "switch", id, "output")
	root := channelTopic(id, "output")
	if _, err := client.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{
		{Topic: controlTopic, QoS: 1}, {Topic: root + "/#", QoS: 1}, {Topic: linkTopic(id), QoS: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	receive := func(topic, payload string) *paho.Publish {
		t.Helper()
		for {
			select {
			case msg := <-observed:
				if msg.Topic == topic && (payload == "*" || string(msg.Payload) == payload) {
					return msg
				}
			case <-ctx.Done():
				t.Fatalf("waiting for %s to receive %q: %v", topic, payload, ctx.Err())
			}
		}
	}
	command := func(topic string, retain bool) {
		t.Helper()
		qos := byte(0)
		if retain {
			qos = 1 // Broker forwards at subscription QoS 0, with RETAIN preserved.
		}
		if _, err := client.Publish(ctx, &paho.Publish{Topic: topic, Retain: retain, QoS: qos, Payload: []byte("ON")}); err != nil {
			t.Fatal(err)
		}
	}
	plc := &simulatedPLC{writes: make(chan bool, 8)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	start := func() func() {
		t.Helper()
		runCtx, stop := context.WithCancel(ctx)
		t.Cleanup(stop)
		done := make(chan error, 1)
		go func() {
			done <- Run(runCtx, cfg, func(context.Context) (bridge.Device, error) { return plc, nil }, log)
		}()
		return func() {
			t.Helper()
			stop()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("bridge did not stop")
			}
		}
	}
	ready := func() string {
		t.Helper()
		msg := receive(controlTopic, "*")
		var conf struct {
			Topic string `json:"command_topic"`
		}
		if err := json.Unmarshal(msg.Payload, &conf); err != nil || conf.Topic == "" {
			t.Fatalf("invalid discovery message %q: %v", msg.Payload, err)
		}
		receive(root+"/state", "OFF")
		receive(root+"/availability", "online")
		receive(root+"/control", "online")
		return conf.Topic
	}
	noWrite := func() {
		t.Helper()
		select {
		case on := <-plc.writes:
			t.Fatalf("unexpected PLC write: %v", on)
		case <-time.After(150 * time.Millisecond):
		}
	}

	stop := start()
	oldTopic := ready()
	command(oldTopic, true) // Live retained publication must never change Q1.
	noWrite()
	stop()
	receive(linkTopic(id), "offline")

	stop = start()
	newTopic := ready()
	if oldTopic == newTopic {
		t.Fatal("command topic survived session restart")
	}
	command(oldTopic, false) // Stale discovery or delayed publisher.
	noWrite()
	command(newTopic, false)
	receive(root+"/state", "ON")
	select {
	case on := <-plc.writes:
		if !on {
			t.Fatal("ON command did not reach the fake PLC")
		}
	default:
		t.Fatal("published ON without applying it")
	}
	plc.blank.Store(true)
	receive(root+"/availability", "offline")
	receive(root+"/state", "")
	noWrite()

	plc.blank.Store(false)
	receive(root+"/state", "ON")
	receive(root+"/control", "online")

	// A second connection with the same ID makes the broker drop the bridge
	// connection. The running process must reconnect with control disarmed.
	takeoverConn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer takeoverConn.Close()
	takeover := paho.NewClient(paho.ClientConfig{Conn: takeoverConn})
	if _, err := takeover.Connect(ctx, &paho.Connect{ClientID: "zelio-" + id, CleanStart: true}); err != nil {
		t.Fatal(err)
	}
	if err := takeover.Disconnect(&paho.Disconnect{}); err != nil {
		t.Fatal(err)
	}
	receive(linkTopic(id), "offline")
	receive(root+"/control", "offline")
	replacement := receive(controlTopic, "*")
	if !strings.Contains(string(replacement.Payload), `"command_topic":`) ||
		strings.Contains(string(replacement.Payload), newTopic) {
		t.Fatalf("expected a rotated command topic after MQTT reconnect: %s", replacement.Payload)
	}
	receive(root+"/state", "ON")
	receive(root+"/availability", "online")
	noWrite()
	stop()
}
