package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestOfflineCodecs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"encode-sl-write", "--word", "3", "--values", "8569"}, "01100000FF0202217953"},
		{[]string{"encode-monitor-force", "--id", "0x2a", "--kind", "force", "--state", "ON"}, "0110000079001EE02A01"},
		{[]string{"encode-monitor-force", "--kind", "release-all"}, "0110000079001E0000"},
	} {
		var out bytes.Buffer
		if err := run(context.Background(), tc.args, &out); err != nil || !strings.Contains(out.String(), tc.want) {
			t.Fatalf("%v: %v %q", tc.args, err, out.String())
		}
	}
}

func TestInvalidArgumentsFailBeforeHardware(t *testing.T) {
	for _, args := range [][]string{
		{"encode-sl-write", "--word", "25", "--values", "1"},
		{"encode-sl-write", "--word", "1", "--values", "65536"},
		{"encode-sl-write", "--word", "1", "--values", "1", "--apply"},
		{"read-sl", "--word", "49"},
		{"read-sl", "--word", "25", "--timeout", "-1s"},
		{"list", "junk"},
		{"force-q1", "--state", "on", "--apply", "--confirm-mapping", "--serial", "TEST-CABLE"},
		{"force-q1", "--state", "ON", "--confirm-mapping", "--serial", "TEST-CABLE"},
		{"force-q1", "--state", "ON", "--apply", "--serial", "TEST-CABLE"},
		{"force-q1", "--state", "ON", "--apply", "--confirm-mapping"},
		{"release-q1", "--apply", "--confirm-mapping", "--state", "OFF", "--serial", "TEST-CABLE"},
		{"release-all", "--apply"},
	} {
		var out bytes.Buffer
		if err := run(context.Background(), args, &out); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
