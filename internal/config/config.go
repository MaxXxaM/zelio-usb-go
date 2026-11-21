package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/MaxXxaM/zelio-usb-go/bridge"
)

type File struct {
	DeviceID     string           `json:"device_id"`
	DeviceName   string           `json:"device_name"`
	CableSerial  string           `json:"cable_serial"`
	MQTT         MQTT             `json:"mqtt"`
	Channels     []bridge.Binding `json:"channels"`
	PollInterval string           `json:"poll_interval"`
	IOTimeout    string           `json:"io_timeout"`
	RetryDelay   string           `json:"retry_delay"`

	poll, timeout, retry time.Duration
}

type MQTT struct {
	URL             string `json:"url"`
	DiscoveryPrefix string `json:"discovery_prefix"`
	UsernameEnv     string `json:"username_env"`
	PasswordEnv     string `json:"password_env"`
	CAFile          string `json:"ca_file"`
}

func (f File) Timeout() time.Duration { return f.timeout }

func (f File) Settings() bridge.Settings {
	return bridge.Settings{Bindings: f.Channels, PollInterval: f.poll, RetryDelay: f.retry}
}

func (f File) HasControl() bool {
	for _, b := range f.Channels {
		if b.CanControl() {
			return true
		}
	}
	return false
}

func Read(path string) (File, error) {
	f, err := os.Open(path)
	if err != nil {
		return File{}, err
	}
	defer f.Close()
	return Decode(f)
}

const maxConfigSize = 64 << 10

func Decode(r io.Reader) (File, error) {
	var f File
	data, err := io.ReadAll(io.LimitReader(r, maxConfigSize+1))
	if err != nil {
		return f, err
	}
	if len(data) > maxConfigSize {
		return f, errors.New("configuration exceeds 64 KiB")
	}
	if err := uniqueKeys(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return f, err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&f); err != nil {
		return f, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return f, errors.New("expected exactly one configuration object")
	}
	if !bridge.ValidID(f.DeviceID) || strings.TrimSpace(f.DeviceName) == "" {
		return f, errors.New("invalid or missing device_id/device_name")
	}
	if f.CableSerial == "" || strings.TrimSpace(f.CableSerial) != f.CableSerial ||
		len(f.CableSerial) > 128 || strings.ContainsFunc(f.CableSerial, unicode.IsControl) {
		return f, errors.New("cable_serial must be an explicit USB cable serial")
	}
	if err := bridge.ValidateBindings(f.Channels); err != nil {
		return f, err
	}
	if f.MQTT.DiscoveryPrefix == "" {
		f.MQTT.DiscoveryPrefix = "homeassistant"
	}
	if !bridge.ValidID(f.MQTT.DiscoveryPrefix) {
		return f, errors.New("mqtt.discovery_prefix must be a single topic segment")
	}
	broker, err := f.MQTT.BrokerURL()
	if err != nil {
		return f, err
	}
	if f.MQTT.CAFile != "" && broker.Scheme != "mqtts" {
		return f, errors.New("mqtt.ca_file requires an mqtts:// URL")
	}
	for _, key := range []string{f.MQTT.UsernameEnv, f.MQTT.PasswordEnv} {
		if key != "" && !envName.MatchString(key) {
			return f, errors.New("invalid MQTT environment variable name")
		}
	}
	if f.poll, err = duration("poll_interval", f.PollInterval, 2*time.Second, 500*time.Millisecond, time.Minute); err != nil {
		return f, err
	}
	if f.timeout, err = duration("io_timeout", f.IOTimeout, time.Second, 50*time.Millisecond, 10*time.Second); err != nil {
		return f, err
	}
	f.retry, err = duration("retry_delay", f.RetryDelay, 3*time.Second, 2*time.Second, time.Minute)
	return f, err
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (m MQTT) BrokerURL() (*url.URL, error) {
	u, err := url.Parse(m.URL)
	if err != nil || (u.Scheme != "mqtt" && u.Scheme != "mqtts") ||
		u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery ||
		strings.Contains(m.URL, "#") || strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("mqtt.url must be mqtt://host[:port] or mqtts://host[:port], with credentials in environment variables")
	}
	if port := u.Port(); port != "" {
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value == 0 {
			return nil, errors.New("invalid MQTT port")
		}
	}
	return u, nil
}

func duration(key, value string, fallback, min, max time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d < min || d > max {
		return 0, fmt.Errorf("%s must be a duration between %s and %s", key, min, max)
	}
	return d, nil
}

// uniqueKeys rejects ambiguous duplicates, including field names the standard
// decoder would match case-insensitively.
func uniqueKeys(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	open, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	seen := make(map[string]bool)
	for d.More() {
		if open == '{' {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("expected configuration property")
			}
			folded := strings.ToLower(name)
			if seen[folded] {
				return fmt.Errorf("duplicate configuration property %q", name)
			}
			seen[folded] = true
		}
		if err := uniqueKeys(d); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}
