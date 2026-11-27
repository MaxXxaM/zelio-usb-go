package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	zelio "github.com/MaxXxaM/zelio-usb-go"
)

func discoveryFixture() zelio.FBDDiscovery {
	return zelio.FBDDiscovery{
		Outputs: []string{"Q4", "Q8", "Q9"},
		Routes: []zelio.FBDRoute{
			{Input: "I6", SignalID: 4, Kind: zelio.FBDToggleCandidate, Outputs: []string{"Q8", "Q9"}},
			{Input: "I7", SignalID: 7, Kind: zelio.FBDToggleCandidate, Outputs: []string{"Q8", "Q9"}},
			{Input: "I1", SignalID: 0x0b, Kind: zelio.FBDReset, Outputs: []string{"Q4", "Q8", "Q9"}, ResetOutputs: []string{"Q8", "Q9"}},
		},
	}
}

func TestDiscoverBuildsReadOnlyUnlessExplicitlyVerified(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	for _, signals := range [][]uint8{nil, {4}} {
		cfg, err := discoveryConfig(discoveryFixture(), "ZELIO-TEST", "test_relay", "mqtt://127.0.0.1", fingerprint, signals)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.FBDSHA256 != fingerprint || cfg.HasControl() != (len(signals) != 0) {
			t.Fatalf("wrong fingerprint or control state: %+v", cfg)
		}
		if len(signals) == 0 {
			if len(cfg.Channels) != 3 {
				t.Fatalf("expected three independent read-only Q channels: %+v", cfg.Channels)
			}
			continue
		}
		if len(cfg.Channels) != 2 || !cfg.Channels[1].CanMomentary() ||
			cfg.Channels[1].SignalID != 4 || len(cfg.Channels[1].Labels) != 2 ||
			len(cfg.Channels[1].BlockWhen) != 1 || cfg.Channels[1].BlockWhen[0] != "I1" {
			t.Fatalf("invalid track group: %+v", cfg.Channels)
		}
	}
}

func TestDiscoverRejectsResetUnknownAndOverlappingSignals(t *testing.T) {
	for _, signals := range [][]uint8{{0x0b}, {0x55}, {4, 7}, {4, 4}} {
		if _, err := discoveryConfig(discoveryFixture(), "ZELIO-TEST", "test_relay", "mqtt://127.0.0.1", "", signals); err == nil {
			t.Fatalf("accepted %v", signals)
		}
	}
}

func TestDiscoverRefusesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.json")
	if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeNewConfig(path, []byte("new")); err == nil {
		t.Fatal("overwrote existing file")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "existing" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestDiscoverRejectsInvalidOptionsBeforeUSB(t *testing.T) {
	for _, args := range [][]string{
		{"discover"},
		{"discover", "--serial", "DEV", "--mqtt-url", "http://invalid"},
		{"discover", "--serial", "DEV", "--device-id", "."},
		{"discover", "--serial", "DEV", "--verified-signal", "0"},
	} {
		var out bytes.Buffer
		if err := run(context.Background(), args, &out); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestVerifySignalFlagUsesDecimalUnlessHexadecimal(t *testing.T) {
	for input, want := range map[string]uint8{"010": 10, "0x10": 16, "0X0b": 11, "255": 255} {
		got, err := parseVerifiedSignal(input)
		if err != nil || got != want {
			t.Fatalf("%q: got %d (%v), want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"0b10", "0o10", "0", "0x00", "256", "-1", "12_3"} {
		if _, err := parseVerifiedSignal(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}

func TestDiscoverRequiresVerifiedProgramBeforeUSB(t *testing.T) {
	var out bytes.Buffer
	err := run(context.Background(), []string{"discover", "--serial", "DEV", "--verified-signal", "4"}, &out)
	if err == nil || !strings.Contains(err.Error(), "--expect-program") {
		t.Fatalf("got %v, want verification hash error", err)
	}
}
