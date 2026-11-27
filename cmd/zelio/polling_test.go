package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
	"time"

	zelio "github.com/MaxXxaM/zelio-usb-go"
)

type monitoringPollingTransport struct {
	requests []byte
	response []byte
	failAt   byte
}

func (w *monitoringPollingTransport) Write(_ context.Context, data []byte) (int, error) {
	packet, err := hex.DecodeString(string(data[1 : len(data)-2]))
	if err != nil {
		return 0, err
	}
	region, count := packet[4], packet[6]
	w.requests = append(w.requests, region)
	if region == w.failAt {
		return 0, errors.New("test transport fault")
	}
	payload := make([]byte, count)
	if region == 0x6f {
		copy(payload, bytes.Repeat([]byte(" "), len(payload)))
		copy(payload, "123456789ABCDEFGHJKLNPQR")
		copy(payload[27:], "RUN")
		copy(payload[54:], "123456789ABCDEFG")
		payload[54] = 0x81
	}
	if region == 0x6b {
		payload[0] = 0x83
	}
	reply := append([]byte{1, 3, count}, payload...)
	var lrc byte
	for _, v := range reply {
		lrc -= v
	}
	w.response = []byte(":" + hex.EncodeToString(append(reply, lrc)) + "\r\n")
	return len(data), nil
}

func (w *monitoringPollingTransport) Read(_ context.Context, buf []byte) (int, error) {
	n := copy(buf, w.response)
	w.response = w.response[n:]
	return n, nil
}

func (w *monitoringPollingTransport) Close() error { return nil }

func TestMonitoringRefreshReads(t *testing.T) {
	for _, fault := range []byte{0, 0x70, 0x6b} {
		wire := &monitoringPollingTransport{failAt: fault}
		client, err := zelio.NewClient(wire, zelio.Options{Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		device := monitoringRefreshClient{Client: client}
		snapshot, err := device.ReadMonitoringIO(context.Background())
		if fault == 0 {
			if on, available := snapshot.Lookup("Q1"); err != nil || !available || !on {
				t.Fatalf("expected fresh Q1 on, got %v %v %v", on, available, err)
			}
		} else if err == nil || snapshot.Available {
			t.Fatalf("region %02x failure was hidden: %+v %v", fault, snapshot, err)
		}
		want := []byte{0x6f, 0x70, 0x6b}
		if fault == 0x70 {
			want = want[:2]
		}
		if !reflect.DeepEqual(wire.requests, want) {
			t.Fatalf("got reads %x, want %x", wire.requests, want)
		}
	}
}
