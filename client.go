package zelio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

var (
	ErrReadOnly        = errors.New("Monitoring writes are disabled")
	ErrUnverifiedWrite = errors.New("live Monitoring frame is outside the verified write allowlist")
	ErrWriteUncertain  = errors.New("write outcome unknown; do not blindly retry")
	ErrDesynchronized  = errors.New("serial session desynchronized; close and reopen")
	ErrClosed          = errors.New("Zelio client closed")
)

// Transport carries UART bytes, without USB FTDI status headers. One client
// exclusively owns one transport; do not share the cable with another process.
type Transport interface {
	Read(context.Context, []byte) (int, error)
	Write(context.Context, []byte) (int, error)
	Close() error
}
type Options struct {
	Timeout time.Duration
	// AllowMonitoringWrites asserts that the operator has verified Q1 = 0x2A
	// on this exact PLC program and accepts force-and-maintain behavior.
	AllowMonitoringWrites bool
}
type Client struct {
	transport     Transport
	options       Options
	gate          chan struct{}
	dirty, closed bool
}

func NewClient(t Transport, opts Options) (*Client, error) {
	if t == nil {
		return nil, errors.New("nil transport")
	}
	if opts.Timeout < 0 {
		return nil, errors.New("timeout must be positive")
	}
	if opts.Timeout == 0 {
		opts.Timeout = time.Second
	}
	c := &Client{transport: t, options: opts, gate: make(chan struct{}, 1)}
	c.gate <- struct{}{}
	return c, nil
}

func (c *Client) lock(ctx context.Context) (context.Context, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, c.options.Timeout)
	select {
	case <-ctx.Done():
		cancel()
		return nil, nil, ctx.Err()
	case <-c.gate:
	}
	unlock := func() { c.gate <- struct{}{}; cancel() }
	if err := ctx.Err(); err != nil {
		unlock()
		return nil, nil, err
	}
	if c.closed {
		unlock()
		return nil, nil, ErrClosed
	}
	if c.dirty {
		unlock()
		return nil, nil, ErrDesynchronized
	}
	return ctx, unlock, nil
}

// exchangeLocked makes exactly one request. SL frames contain no transaction ID;
// after a timeout a late reply cannot safely be matched to a subsequent request.
func (c *Client) exchangeLocked(ctx context.Context, request []byte) (reply []byte, sent bool, err error) {
	if err = ctx.Err(); err != nil {
		return nil, false, err
	}
	defer func() {
		if err != nil {
			c.dirty = true
		}
	}()
	n, err := c.transport.Write(ctx, request)
	if err != nil {
		return nil, true, err
	}
	if n != len(request) {
		return nil, true, io.ErrShortWrite
	}
	var chunk [256]byte
	for {
		if err = ctx.Err(); err != nil {
			return nil, true, err
		}
		n, err = c.transport.Read(ctx, chunk[:])
		if err != nil {
			return nil, true, err
		}
		if n < 0 || n > len(chunk) {
			return nil, true, fmt.Errorf("%w: invalid read length", ErrProtocol)
		}
		reply = append(reply, chunk[:n]...)
		if len(reply) > 512 {
			return nil, true, fmt.Errorf("%w: oversized frame", ErrProtocol)
		}
		if end := bytes.Index(reply, []byte("\r\n")); end >= 0 {
			if end != len(reply)-2 {
				return nil, true, fmt.Errorf("%w: trailing data", ErrProtocol)
			}
			return reply, true, nil
		}
		if n == 0 {
			select {
			case <-ctx.Done():
				return nil, true, ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
	}
}

func (c *Client) readLocked(ctx context.Context, address, count int) ([]uint16, error) {
	request, err := ReadRequest(address, count)
	if err != nil {
		return nil, err
	}
	response, _, err := c.exchangeLocked(ctx, request)
	if err != nil {
		return nil, err
	}
	words, err := ParseReadResponse(response, count)
	if err != nil {
		c.dirty = true
	}
	return words, err
}

func (c *Client) ReadWords(ctx context.Context, address, count int) ([]uint16, error) {
	if _, err := ReadRequest(address, count); err != nil {
		return nil, err
	}
	ctx, unlock, err := c.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return c.readLocked(ctx, address, count)
}

func (c *Client) Close() error {
	<-c.gate
	defer func() { c.gate <- struct{}{} }()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.transport.Close()
}
