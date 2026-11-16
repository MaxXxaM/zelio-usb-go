// Package sr2usb accesses the observed SR2USB01 FT232R-class USB cable directly.
package sr2usb

import "fmt"

// FTDI prefixes EVERY USB packet, including status-only packets, with two bytes.
// Line errors must not be mistaken for valid Zelio data.
func stripStatus(data []byte, maxPacket int) ([]byte, error) {
	if maxPacket < 3 {
		return nil, fmt.Errorf("invalid USB packet size %d", maxPacket)
	}
	out := make([]byte, 0, len(data))
	for len(data) > 0 {
		n := len(data)
		if n > maxPacket {
			n = maxPacket
		}
		if n < 2 {
			return nil, fmt.Errorf("truncated FTDI status header")
		}
		if data[1]&0x9e != 0 {
			return nil, fmt.Errorf("FTDI UART line error: status=%02x%02x", data[0], data[1])
		}
		out = append(out, data[2:n]...)
		data = data[n:]
	}
	return out, nil
}
