package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	zelio "github.com/MaxXxaM/zelio-usb-go"
	"github.com/MaxXxaM/zelio-usb-go/bridge"
	"github.com/MaxXxaM/zelio-usb-go/internal/config"
	"github.com/MaxXxaM/zelio-usb-go/sr2usb"
)

func discover(ctx context.Context, fs *flag.FlagSet, parse func() error, out io.Writer) error {
	serial := fs.String("serial", "", "USB cable serial")
	deviceID := fs.String("device-id", "", "MQTT device ID, defaults to zelio_<serial>")
	mqttURL := fs.String("mqtt-url", "mqtt://127.0.0.1:1883", "broker URL for the draft")
	configOut := fs.String("config-out", "", "write the MQTT draft to a new file")
	expectProgram := fs.String("expect-program", "", "SHA-256 from the report used to verify the mapping")
	timeout := fs.Duration("timeout", time.Second, "PLC transaction timeout")
	var verified []uint8
	fs.Func("verified-signal", "include one physically verified toggle ID (repeatable, decimal or 0x hex)", func(arg string) error {
		id, err := parseVerifiedSignal(arg)
		if err != nil {
			return err
		}
		verified = append(verified, id)
		return nil
	})
	if err := parse(); err != nil {
		return err
	}
	if *serial == "" || strings.TrimSpace(*serial) != *serial {
		return errors.New("discover requires --serial")
	}
	if *deviceID == "" {
		*deviceID = "zelio_" + strings.ToLower(*serial)
	}
	if !bridge.ValidID(*deviceID) {
		return errors.New("invalid --device-id")
	}
	if _, err := (config.MQTT{URL: *mqttURL}).BrokerURL(); err != nil {
		return err
	}
	if (*expectProgram != "" || len(verified) > 0) && !config.ValidProgramFingerprint(*expectProgram) {
		return errors.New("--expect-program must be the lowercase program SHA-256 from the discovery report")
	}
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	wire, err := sr2usb.Open(*serial, sr2usb.UARTOptions{DTR: true, RTS: true})
	if err != nil {
		return err
	}
	c, err := client(wire, zelio.Options{Timeout: *timeout})
	if err != nil {
		return err
	}
	defer c.Close()
	links, code, err := c.ReadFBDProgram(ctx)
	if err != nil {
		return err
	}
	hash := fbdFingerprint(links, code)
	if *expectProgram != "" && hash != *expectProgram {
		return errors.New("FBD fingerprint changed; verify the bindings again")
	}
	found, err := zelio.DecodeMonitoringFBD(links, code)
	if err != nil {
		return err
	}
	cfg, err := discoveryConfig(found, *serial, *deviceID, *mqttURL, hash, verified)
	if err != nil {
		return err
	}
	if *configOut != "" {
		var contents bytes.Buffer
		if err := output(&contents, cfg); err != nil {
			return err
		}
		if err := writeNewConfig(*configOut, contents.Bytes()); err != nil {
			return err
		}
	}
	return output(out, struct {
		zelio.FBDDiscovery
		ProgramSHA256 string      `json:"program_sha256"`
		Config        config.File `json:"config"`
	}{found, hash, cfg})
}

func parseVerifiedSignal(input string) (uint8, error) {
	base := 10
	if strings.HasPrefix(input, "0x") || strings.HasPrefix(input, "0X") {
		base, input = 16, input[2:]
	}
	id, err := strconv.ParseUint(input, base, 8)
	if err != nil || id == 0 {
		return 0, errors.New("expected a nonzero decimal or 0x hexadecimal signal ID from 1 to 255")
	}
	return uint8(id), nil
}

func fbdFingerprint(links, code []byte) string {
	hash := sha256.New()
	_, _ = hash.Write(links)
	_, _ = hash.Write(code)
	return hex.EncodeToString(hash.Sum(nil))
}

func verifyFBDProgram(ctx context.Context, c *zelio.Client, expected string) error {
	links, code, err := c.ReadFBDProgram(ctx)
	if err != nil {
		return err
	}
	if fbdFingerprint(links, code) != expected {
		return errors.New("FBD fingerprint changed; run discover and verify the bindings again")
	}
	return nil
}

func discoveryConfig(found zelio.FBDDiscovery, serial, id, url, fingerprint string, verified []uint8) (config.File, error) {
	cfg := config.File{
		DeviceID: id, DeviceName: "Zelio Logic " + serial, CableSerial: serial,
		MQTT: config.MQTT{URL: url, DiscoveryPrefix: "homeassistant"}, FBDSHA256: fingerprint,
	}
	routes := make(map[uint8]zelio.FBDRoute)
	var resets []zelio.FBDRoute
	for _, route := range found.Routes {
		if route.Kind == zelio.FBDToggleCandidate {
			routes[route.SignalID] = route
		}
		if route.Kind == zelio.FBDReset && route.Input != "" {
			resets = append(resets, route)
		}
	}
	claimed := make(map[string]bool)
	var writable []bridge.Binding
	for _, id := range verified {
		route, ok := routes[id]
		if !ok || id == 0 || len(route.Outputs) == 0 {
			return cfg, fmt.Errorf("signal 0x%02X is not a supported toggle candidate", id)
		}
		for _, label := range route.Outputs {
			if claimed[label] {
				return cfg, fmt.Errorf("signal 0x%02X overlaps another verified signal at %s", id, label)
			}
			claimed[label] = true
		}
		var interlocks []string
		for _, reset := range resets {
			if slices.Contains(interlocks, reset.Input) {
				continue
			}
			for _, label := range route.Outputs {
				if slices.Contains(reset.ResetOutputs, label) {
					interlocks = append(interlocks, reset.Input)
					break
				}
			}
		}
		slices.Sort(interlocks)
		binding := bridge.Binding{
			ID: strings.ToLower(strings.Join(route.Outputs, "_")), Name: strings.Join(route.Outputs, " + "),
			Control: bridge.VerifiedMomentary, SignalID: id, BlockWhen: interlocks,
		}
		if len(route.Outputs) == 1 {
			binding.Label = route.Outputs[0]
		} else {
			binding.Labels = route.Outputs
		}
		writable = append(writable, binding)
	}
	for _, label := range found.Outputs {
		if !claimed[label] {
			cfg.Channels = append(cfg.Channels, bridge.Binding{ID: strings.ToLower(label), Name: label, Label: label})
		}
	}
	cfg.Channels = append(cfg.Channels, writable...)
	data, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	return config.Decode(bytes.NewReader(data))
}

func writeNewConfig(path string, contents []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(contents)
	err = errors.Join(err, file.Close())
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}
