package zelio

import (
	"bytes"
	"errors"
	"testing"
)

// Literal vectors from Schneider EIO0000002612, printed pages 334 and 336.
func TestSchneiderVectors(t *testing.T) {
	w, err := WriteRequest(3, []uint16{8569})
	if err != nil || string(w) != ":01100000FF0202217953\r\n" {
		t.Fatalf("write=%q err=%v", w, err)
	}
	if err := ValidateWriteResponse([]byte(":01100000FF0202ED\r\n"), 3, 1); err != nil {
		t.Fatal(err)
	}
	r, err := ReadRequest(17, 5)
	if err != nil || string(r) != ":01030000FF100AE4\r\n" {
		t.Fatalf("read=%q err=%v", r, err)
	}
	values, err := ParseReadResponse([]byte(":01030A00000000000000000000F3\r\n"), 5)
	if err != nil || len(values) != 5 {
		t.Fatalf("read response=%v err=%v", values, err)
	}
	for _, v := range values {
		if v != 0 {
			t.Fatal(values)
		}
	}
}

func TestReadRejectsInvalidReplies(t *testing.T) {
	for _, frame := range []string{
		"", ":01030A00000000000000000000F2\r\n", // bad checksum
		":01030A00000000000000000000F3\n", // wrong delimiters
		":01030A00000000000000000000F3\r\nextra",
		":0103020000FB\r\n",                 // valid frame, unexpected count
		":02030A00000000000000000000F2\r\n", // different slave
		":01040A00000000000000000000F2\r\n", // different function
		":01030A00000000000000000000ZZ\r\n",
		":01 030A00000000000000000000F3\r\n", // whitespace is not hex
	} {
		if _, err := ParseReadResponse([]byte(frame), 5); !errors.Is(err, ErrProtocol) {
			t.Errorf("accepted %q: %v", frame, err)
		}
	}
}

func TestAddressAndCountBounds(t *testing.T) {
	for _, tc := range [][2]int{{0, 1}, {49, 1}, {48, 2}, {1, 0}, {1, 49}, {-1, 1}} {
		if _, err := ReadRequest(tc[0], tc[1]); err == nil {
			t.Errorf("accepted %v", tc)
		}
	}
	for _, tc := range []struct {
		address int
		values  []uint16
	}{{0, []uint16{1}}, {25, []uint16{1}}, {24, []uint16{1, 2}}, {1, nil}} {
		if _, err := WriteRequest(tc.address, tc.values); err == nil {
			t.Errorf("accepted %+v", tc)
		}
	}
	if _, err := ReadRequest(1, 48); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteRequest(24, []uint16{65535}); err != nil {
		t.Fatal(err)
	}
}

func TestWriteAckMustMatchRequest(t *testing.T) {
	ack := []byte(":01100000FF0202ED\r\n")
	for _, tc := range [][2]int{{4, 1}, {3, 2}} {
		if err := ValidateWriteResponse(ack, tc[0], tc[1]); err == nil {
			t.Fatal("accepted wrong ACK")
		}
	}
}

func TestChecksumWrapAndLeadingZero(t *testing.T) {
	// Sum 0x100 must produce 01, sum 1 must produce 00, sum 0x101 also 00.
	for _, tc := range []struct {
		in   []byte
		want byte
	}{{[]byte{255, 1}, 1}, {[]byte{1}, 0}, {[]byte{255, 2}, 0}} {
		if got := checksum(tc.in); got != tc.want {
			t.Fatalf("got %x want %x", got, tc.want)
		}
	}
	r, _ := ReadRequest(25, 1)
	if !bytes.Equal(r, []byte(":01030000FF1802E4\r\n")) {
		t.Fatalf("%q", r)
	}
}
