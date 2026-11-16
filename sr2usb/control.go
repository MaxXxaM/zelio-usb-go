package sr2usb

import "fmt"

type controlDevice interface {
	Control(uint8, uint8, uint16, uint16, []byte) (int, error)
}
type UARTOptions struct{ DTR, RTS bool }
type Status struct {
	ModemStatus string `json:"modem_status_hex"`
	LatencyMS   uint8  `json:"latency_ms"`
}

// probeControl uses IN transfers only: no UART bytes, reset, purge, or pin changes.
func probeControl(d controlDevice) (Status, error) {
	var status Status
	modem := make([]byte, 2)
	n, err := d.Control(0xc0, 5, 0, 1, modem)
	if err != nil {
		return status, fmt.Errorf("FTDI status: %w", err)
	}
	if n != 2 {
		return status, fmt.Errorf("FTDI status: expected 2 bytes, got %d", n)
	}
	latency := make([]byte, 1)
	n, err = d.Control(0xc0, 10, 0, 1, latency)
	if err != nil {
		return status, fmt.Errorf("FTDI latency: %w", err)
	}
	if n != 1 {
		return status, fmt.Errorf("FTDI latency: expected 1 byte, got %d", n)
	}
	status.ModemStatus = fmt.Sprintf("%x", modem)
	status.LatencyMS = latency[0]
	return status, nil
}

// configureUART applies FT232R UART settings (not PLC programming commands).
// FTDI request encodings: Linux drivers/usb/serial/ftdi_sio.h; divisor matches
// PyFtdi's FT232R conversion for 115200. Other FTDI families are not assumed.
func configureUART(d controlDevice, opts UARTOptions) error {
	modem := uint16(0x0300)
	if opts.DTR {
		modem |= 1
	}
	if opts.RTS {
		modem |= 2
	}
	steps := []struct {
		request      uint8
		value, index uint16
	}{
		{0, 0, 1}, {11, 0, 1}, {3, 26, 0}, {4, 0x0207, 1}, {2, 0, 1}, {9, 2, 1}, {1, modem, 1}, {0, 1, 1}, {0, 2, 1},
	}
	for _, s := range steps {
		n, err := d.Control(0x40, s.request, s.value, s.index, nil)
		if err != nil {
			return fmt.Errorf("FTDI setup request %02x: %w", s.request, err)
		}
		if n != 0 {
			return fmt.Errorf("FTDI setup request %02x: unexpected length %d", s.request, n)
		}
	}
	return nil
}
