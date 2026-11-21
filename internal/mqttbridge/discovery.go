package mqttbridge

import (
	"fmt"

	"github.com/MaxXxaM/zelio-usb-go/bridge"
)

func base(device string) string             { return "zelio/" + device }
func linkTopic(device string) string        { return base(device) + "/availability" }
func channelTopic(device, id string) string { return base(device) + "/" + id }
func commandTopic(device, nonce, id string) string {
	return base(device) + "/command/" + nonce + "/" + id
}

func component(binding bridge.Binding) string {
	if binding.CanControl() {
		return "switch"
	}
	return "binary_sensor"
}

func configTopic(prefix, kind, device, id string) string {
	return fmt.Sprintf("%s/%s/%s/%s/config", prefix, kind, device, id)
}

func discovery(device, name, nonce string, binding bridge.Binding) map[string]any {
	root := channelTopic(device, binding.ID)
	availability := []map[string]string{
		{
			"topic":          linkTopic(device),
			"value_template": "{{ 'online' if value == 'online/" + nonce + "' else 'offline' }}",
		},
		{"topic": root + "/availability"},
	}
	if binding.CanControl() {
		availability = append(availability, map[string]string{"topic": root + "/control"})
	}
	cfg := map[string]any{
		"name":              binding.Name,
		"unique_id":         "zelio_" + device + "/" + binding.ID,
		"state_topic":       root + "/state",
		"availability":      availability,
		"availability_mode": "all",
		"payload_on":        "ON",
		"payload_off":       "OFF",
		"qos":               0,
		"device": map[string]any{
			"identifiers":  []string{"zelio_" + device},
			"name":         name,
			"manufacturer": "Schneider Electric",
			"model":        "Zelio Logic",
		},
	}
	if binding.CanControl() {
		cfg["command_topic"] = commandTopic(device, nonce, binding.ID)
		cfg["optimistic"] = false
		cfg["retain"] = false
	}
	return cfg
}
