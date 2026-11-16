package config

import (
	"strings"
	"testing"
	"time"
)

const example = `{
    "device_id":"test_relay",
    "device_name":"Test relay",
    "cable_serial":"TEST-CABLE",
    "mqtt":{"url":"mqtts://broker.example","password_env":"TEST_PASSWORD"},
    "channels":[{"id":"output","name":"Output 1","label":"Q1"}]
}`

func TestDefaultsAndInvalidInput(t *testing.T) {
	cfg, err := Decode(strings.NewReader(example))
	if err != nil || cfg.Settings().PollInterval != 2*time.Second || cfg.Timeout() != time.Second ||
		cfg.Channels[0].CanForce() || cfg.MQTT.DiscoveryPrefix != "homeassistant" {
		t.Fatalf("%+v %v", cfg, err)
	}
	for _, candidate := range []string{
		example + `{}`,
		strings.Replace(example, "mqtts://broker.example", "mqtts://user:pass@broker.example", 1),
		strings.Replace(example, "mqtts://broker.example", "mqtts://broker.example/something", 1),
		strings.Replace(example, "mqtts://broker.example", "mqtts://broker.example:0", 1),
		strings.Replace(example, `"password_env":`, `"password":`, 1),
		strings.Replace(example, `"device_id":`, `"cable_serial":"", "device_id":`, 1),
		strings.Replace(example, `"TEST-CABLE"`, `""`, 1),
		strings.Replace(example, `"device_id":`, `"poll_interval":"1ms","device_id":`, 1),
	} {
		if _, err := Decode(strings.NewReader(candidate)); err == nil {
			t.Errorf("accepted %s", candidate)
		}
	}
}
