package sr2usb

import (
	"bytes"
	"testing"
)

func TestStripFTDIStatusEachPacket(t *testing.T) {
	data := append([]byte{1, 0x60}, bytes.Repeat([]byte{'x'}, 62)...)
	data = append(data, 1, 0x60, 'y', 'z')
	got, err := stripStatus(data, 64)
	want := append(bytes.Repeat([]byte{'x'}, 62), 'y', 'z')
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%q %v", got, err)
	}
}
func TestStatusOnlyAndLineErrors(t *testing.T) {
	if got, err := stripStatus([]byte{1, 0x60}, 64); err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	for _, status := range []byte{0x62, 0x64, 0x68, 0x70, 0xE0} {
		if _, err := stripStatus([]byte{1, status, 'x'}, 64); err == nil {
			t.Fatalf("accepted error %02x", status)
		}
	}
	if _, err := stripStatus([]byte{1}, 64); err == nil {
		t.Fatal("accepted truncated status")
	}
}
