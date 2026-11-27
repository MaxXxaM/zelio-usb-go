package zelio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

const (
	fbdBankCount = 16
	fbdBankSize  = 256
	fbdPageSize  = 64
	fbdLinkBank  = 0x00
	fbdCodeBank  = 0x18
)

// ReadFBDProgram reads the fixed link and instruction banks observed on the
// SR3B261FU and SR3XT141FU in FBD mode. It performs only read transactions and
// returns the images if two passes agree. No monitoring session is started.
func (c *Client) ReadFBDProgram(ctx context.Context) (links, code []byte, err error) {
	links, code, err = c.readFBDBanks(ctx)
	if err != nil {
		return nil, nil, err
	}
	checkLinks, checkCode, err := c.readFBDBanks(ctx)
	if err != nil {
		return nil, nil, err
	}
	if !bytes.Equal(links, checkLinks) || !bytes.Equal(code, checkCode) {
		return nil, nil, errors.New("FBD program reads differ")
	}
	return links, code, nil
}

func (c *Client) readFBDBanks(ctx context.Context) (links, code []byte, err error) {
	links, err = c.readFBDBankImage(ctx, fbdLinkBank)
	if err != nil {
		return nil, nil, err
	}
	code, err = c.readFBDBankImage(ctx, fbdCodeBank)
	return links, code, err
}

func (c *Client) readFBDBankImage(ctx context.Context, first uint8) ([]byte, error) {
	image := make([]byte, 0, fbdBankCount*fbdBankSize)
	for bank := first; bank < first+fbdBankCount; bank++ {
		for offset := 0; offset < fbdBankSize; offset += fbdPageSize {
			page, err := c.readFBDPage(ctx, bank, uint8(offset))
			if err != nil {
				return nil, fmt.Errorf("read FBD bank 0x%02X offset 0x%02X: %w", bank, offset, err)
			}
			image = append(image, page...)
		}
	}
	return image, nil
}

func (c *Client) readFBDPage(ctx context.Context, bank, offset uint8) ([]byte, error) {
	request, _ := MonitoringReadRequest(bank, offset, fbdPageSize)
	ctx, unlock, err := c.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	reply, _, err := c.exchangeLocked(ctx, request)
	if err != nil {
		return nil, err
	}
	page, err := ParseMonitoringReadResponse(reply, fbdPageSize)
	if err != nil {
		c.dirty = true
	}
	return page, err
}
