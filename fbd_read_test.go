package zelio

import (
	"bytes"
	"context"
	"testing"
)

func TestReadFBDProgramUsesOnlyFixedReadRequests(t *testing.T) {
	var w fakeWire
	for i := range 256 {
		payload := bytes.Repeat([]byte{byte(i % 128)}, 64)
		w.replies = append(w.replies, monitoringEncode(append([]byte{1, 3, 64}, payload...)))
	}
	c := newTestClient(t, &w, false)
	links, code, err := c.ReadFBDProgram(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 4096 || len(code) != 4096 || links[64] != 1 || code[0] != 64 {
		t.Fatalf("unexpected image size or contents: links=%d code=%d", len(links), len(code))
	}
	if len(w.writes) != 256 {
		t.Fatalf("transactions = %d", len(w.writes))
	}
	for i, raw := range w.writes {
		request, err := monitoringDecode(raw)
		j := i % 128
		bank := j / 4
		if bank >= 16 {
			bank += 8
		}
		expected := []byte{1, 3, 0, 0, byte(bank), byte(j % 4 * 64), 64}
		if err != nil || !bytes.Equal(request, expected) {
			t.Fatalf("request %d = % X, want % X, err=%v", i, request, expected, err)
		}
	}
}

func TestReadFBDProgramRejectsChangingImage(t *testing.T) {
	var w fakeWire
	for i := range 256 {
		payload := make([]byte, 64)
		if i == 255 {
			payload[63] = 1
		}
		w.replies = append(w.replies, monitoringEncode(append([]byte{1, 3, 64}, payload...)))
	}
	c := newTestClient(t, &w, false)
	if links, code, err := c.ReadFBDProgram(context.Background()); err == nil || links != nil || code != nil {
		t.Fatalf("expected no result for inconsistent reads: %v", err)
	}
}

func TestReadFBDProgramStopsOnInvalidReply(t *testing.T) {
	w := fakeWire{replies: [][]byte{syntheticIO(false)}}
	c := newTestClient(t, &w, false)
	if _, _, err := c.ReadFBDProgram(context.Background()); err == nil {
		t.Fatal("accepted wrong response length")
	}
	if len(w.writes) != 1 {
		t.Fatalf("tried %d requests after a bad response", len(w.writes))
	}
}
