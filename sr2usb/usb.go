package sr2usb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/google/gousb"
)

const VendorID = 0x16de
const ProductID = 0x0084

type DeviceInfo struct {
	Product   string `json:"product"`
	Serial    string `json:"serial"`
	VID       string `json:"vid"`
	PID       string `json:"pid"`
	BCDDevice string `json:"bcd_device"`
	Bus       int    `json:"bus"`
	Address   int    `json:"address"`
}
type ProbeResult struct {
	Device         DeviceInfo `json:"device"`
	Status         Status     `json:"status"`
	UARTConfigured bool       `json:"uart_configured"`
	PLCBytesSent   int        `json:"plc_bytes_sent"`
}

func info(d *gousb.Device) (DeviceInfo, error) {
	serial, err := d.SerialNumber()
	if err != nil {
		return DeviceInfo{}, err
	}
	product, err := d.Product()
	if err != nil {
		return DeviceInfo{}, err
	}
	return DeviceInfo{product, serial, fmt.Sprintf("%04x", VendorID), fmt.Sprintf("%04x", ProductID), fmt.Sprintf("%04x", uint16(d.Desc.Device)), d.Desc.Bus, d.Desc.Address}, nil
}

func devices(ctx *gousb.Context) ([]*gousb.Device, error) {
	found, err := ctx.OpenDevices(func(d *gousb.DeviceDesc) bool { return d.Vendor == VendorID && d.Product == ProductID })
	if err != nil {
		for _, d := range found {
			d.Close()
		}
		return nil, fmt.Errorf("USB enumeration/access: %w", err)
	}
	for _, d := range found {
		d.ControlTimeout = time.Second
	}
	return found, nil
}

// gousb reports libusb initialization errors as panics.
func usbContext() (_ *gousb.Context, err error) {
	defer func() {
		if v := recover(); v != nil {
			usbErr, ok := v.(gousb.Error)
			if !ok {
				panic(v)
			}
			err = fmt.Errorf("initialize libusb: %w", usbErr)
		}
	}()
	return gousb.NewContext(), nil
}

// List reads USB descriptors only. It does not claim interfaces or configure UART.
func List() ([]DeviceInfo, error) {
	ctx, err := usbContext()
	if err != nil {
		return nil, err
	}
	defer ctx.Close()
	found, err := devices(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, d := range found {
			d.Close()
		}
	}()
	result := make([]DeviceInfo, 0, len(found))
	for _, d := range found {
		i, err := info(d)
		if err != nil {
			return nil, err
		}
		result = append(result, i)
	}
	return result, nil
}

func selectDevice(ctx *gousb.Context, serial string) (selected *gousb.Device, err error) {
	found, err := devices(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, d := range found {
			if err != nil || d != selected {
				d.Close()
			}
		}
	}()
	for _, d := range found {
		s, e := d.SerialNumber()
		if e != nil {
			return nil, e
		}
		if serial != "" && s != serial {
			continue
		}
		if selected != nil {
			return nil, errors.New("multiple matching cables; specify a unique serial number")
		}
		selected = d
	}
	if selected == nil {
		return nil, fmt.Errorf("SR2USB01 16de:0084 not found (serial=%q)", serial)
	}
	if selected.Desc.Device != 0x0600 {
		return nil, fmt.Errorf("unsupported cable bcdDevice %04x; only observed FT232R-class 0600 is implemented", uint16(selected.Desc.Device))
	}
	return selected, nil
}

// Probe performs read-only FTDI control transfers, without opening the UART.
func Probe(serial string) (ProbeResult, error) {
	var result ProbeResult
	ctx, err := usbContext()
	if err != nil {
		return result, err
	}
	defer ctx.Close()
	d, err := selectDevice(ctx, serial)
	if err != nil {
		return result, err
	}
	defer d.Close()
	result.Device, err = info(d)
	if err != nil {
		return result, err
	}
	result.Status, err = probeControl(d)
	return result, err
}

type inputEndpoint interface {
	ReadContext(context.Context, []byte) (int, error)
}
type outputEndpoint interface {
	WriteContext(context.Context, []byte) (int, error)
}

type Transport struct {
	ctx     *gousb.Context
	device  *gousb.Device
	release func()
	in      inputEndpoint
	out     outputEndpoint
	pending []byte
	mu      sync.Mutex
	closed  bool
}

// Open claims interface 0 and configures 115200 7E1. It resets/purges the cable's
// UART and sets DTR/RTS as requested, but sends no bytes to the PLC. No kernel
// driver is forcibly detached: close competing software/unbind it explicitly.
func Open(serial string, opts UARTOptions) (_ *Transport, err error) {
	ctx, err := usbContext()
	if err != nil {
		return nil, err
	}
	t := &Transport{ctx: ctx}
	defer func() {
		if err != nil {
			t.Close()
		}
	}()
	t.device, err = selectDevice(t.ctx, serial)
	if err != nil {
		return nil, err
	}
	var intf *gousb.Interface
	intf, t.release, err = t.device.DefaultInterface()
	if err != nil {
		return nil, fmt.Errorf("claim cable interface: %w", err)
	}
	var input *gousb.InEndpoint
	input, err = intf.InEndpoint(1)
	if err != nil {
		return nil, err
	}
	t.out, err = intf.OutEndpoint(2)
	if err != nil {
		return nil, err
	}
	if input.Desc.MaxPacketSize != 64 {
		return nil, fmt.Errorf("unsupported IN packet size: %d", input.Desc.MaxPacketSize)
	}
	t.in = input
	if err = configureUART(t.device, opts); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *Transport) Status() (Status, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return Status{}, io.ErrClosedPipe
	}
	return probeControl(t.device)
}

func (t *Transport) Read(ctx context.Context, buf []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return 0, io.ErrClosedPipe
	}
	if len(buf) == 0 {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	for len(t.pending) == 0 {
		var packet [64]byte
		n, err := t.in.ReadContext(ctx, packet[:])
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if err != nil {
			return 0, err
		}
		t.pending, err = stripStatus(packet[:n], 64)
		if err != nil {
			return 0, err
		}
	}
	n := copy(buf, t.pending)
	t.pending = t.pending[n:]
	return n, nil
}

func (t *Transport) Write(ctx context.Context, data []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return 0, io.ErrClosedPipe
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	n, err := t.out.WriteContext(ctx, data)
	if ctx.Err() != nil {
		return n, ctx.Err()
	}
	return n, err
}

func (t *Transport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	if t.release != nil {
		t.release()
	}
	var err error
	if t.device != nil {
		err = t.device.Close()
	}
	if t.ctx != nil {
		err = errors.Join(err, t.ctx.Close())
	}
	return err
}
