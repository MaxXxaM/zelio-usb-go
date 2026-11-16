package mqttbridge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MaxXxaM/zelio-usb-go/bridge"
	"github.com/eclipse/paho.golang/paho"
)

func TestCommandFilters(t *testing.T) {
	topics := map[string]string{"zelio/relay/command/current/output": "output"}
	for _, tc := range []struct {
		packet paho.Publish
		accept bool
	}{
		{paho.Publish{Topic: "zelio/relay/command/old/output", Payload: []byte("ON")}, false},
		{paho.Publish{Topic: "zelio/relay/command/current/output", Payload: []byte("ON"), Retain: true}, false},
		{paho.Publish{Topic: "zelio/relay/command/current/output", Payload: []byte("TOGGLE")}, false},
		{paho.Publish{Topic: "zelio/relay/command/current/output", Payload: []byte("OFF"), QoS: 1}, false},
		{paho.Publish{Topic: "zelio/relay/command/current/output", Payload: []byte("ON")}, true},
	} {
		c, ok := decodeCommand(&tc.packet, topics)
		if ok != tc.accept || (ok && c != (bridge.Command{ID: "output", On: true})) {
			t.Fatalf("%+v yielded %+v %v", tc.packet, c, ok)
		}
	}
}

func TestDiscoveryRetainsLogicalStateAndScopesCommands(t *testing.T) {
	ro := bridge.Binding{ID: "input", Name: "Button", Label: "I1"}
	rw := bridge.Binding{ID: "output", Name: "Relay", Label: "Q1", Control: bridge.VerifiedQ1}
	for _, binding := range []bridge.Binding{ro, rw} {
		conf := discovery("relay", "Bench", "nonce", binding)
		raw, err := json.Marshal(conf)
		if err != nil {
			t.Fatal(err)
		}
		s := string(raw)
		if strings.Contains(s, `"command_topic"`) != binding.CanForce() ||
			!strings.Contains(s, `"availability_mode":"all"`) ||
			!strings.Contains(s, `online/nonce`) ||
			!strings.Contains(s, `"state_topic":"zelio/relay/`+binding.ID+`/state"`) {
			t.Fatal(s)
		}
		if binding.CanForce() && (!strings.Contains(s, `"retain":false`) ||
			!strings.Contains(s, `"optimistic":false`) ||
			!strings.Contains(s, `"command_topic":"zelio/relay/command/nonce/output"`)) {
			t.Fatal(s)
		}
	}
}
