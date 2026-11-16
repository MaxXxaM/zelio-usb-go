// Package zelio implements Zelio Logic Monitoring and the documented SL word
// protocol over a caller-owned UART-byte transport. SL addresses are one-based.
package zelio

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var ErrProtocol = errors.New("invalid Zelio response")

// This differs from Modbus ASCII LRC by one. See EIO0000002612 pp. 333–336.
func checksum(data []byte) byte {
	var sum byte
	for _, b := range data {
		sum += b
	}
	return 1 - sum
}

func encode(data []byte) []byte {
	payload := append(append([]byte(nil), data...), checksum(data))
	return []byte(":" + strings.ToUpper(hex.EncodeToString(payload)) + "\r\n")
}

func decode(frame []byte) ([]byte, error) {
	if len(frame) < 9 || frame[0] != ':' || string(frame[len(frame)-2:]) != "\r\n" {
		return nil, fmt.Errorf("%w: framing", ErrProtocol)
	}
	data, err := hex.DecodeString(string(frame[1 : len(frame)-2]))
	if err != nil || len(data) < 3 {
		return nil, fmt.Errorf("%w: hexadecimal encoding", ErrProtocol)
	}
	if checksum(data[:len(data)-1]) != data[len(data)-1] {
		return nil, fmt.Errorf("%w: checksum", ErrProtocol)
	}
	if data[0] != 1 {
		return nil, fmt.Errorf("%w: slave must be 1", ErrProtocol)
	}
	return data[:len(data)-1], nil
}

func validateRange(address, count, limit int) error {
	if address < 1 || address > limit || count < 1 || count > limit-address+1 {
		return fmt.Errorf("SL word range: address=%d count=%d; valid 1..%d", address, count, limit)
	}
	return nil
}

// ReadRequest encodes a read of SL words 1..48 (including SL IN readback).
func ReadRequest(address, count int) ([]byte, error) {
	if err := validateRange(address, count, 48); err != nil {
		return nil, err
	}
	return encode([]byte{1, 3, 0, 0, 255, byte(address - 1), byte(count * 2)}), nil
}

// WriteRequest encodes raw 16-bit words to SL IN addresses 1..24.
func WriteRequest(address int, values []uint16) ([]byte, error) {
	if err := validateRange(address, len(values), 24); err != nil {
		return nil, err
	}
	data := []byte{1, 0x10, 0, 0, 255, byte(address - 1), byte(len(values) * 2)}
	for _, v := range values {
		data = binary.BigEndian.AppendUint16(data, v)
	}
	return encode(data), nil
}

func ParseReadResponse(frame []byte, count int) ([]uint16, error) {
	if count < 1 || count > 48 {
		return nil, fmt.Errorf("invalid word count: %d", count)
	}
	data, err := decode(frame)
	if err != nil {
		return nil, err
	}
	if len(data) != 3+count*2 || data[1] != 3 || int(data[2]) != count*2 {
		return nil, fmt.Errorf("%w: read function or byte count", ErrProtocol)
	}
	values := make([]uint16, count)
	for i := range values {
		values[i] = binary.BigEndian.Uint16(data[3+i*2:])
	}
	return values, nil
}

func ValidateWriteResponse(frame []byte, address, count int) error {
	if err := validateRange(address, count, 24); err != nil {
		return err
	}
	data, err := decode(frame)
	if err != nil {
		return err
	}
	want := []byte{1, 0x10, 0, 0, 255, byte(address - 1), byte(count * 2)}
	if string(data) != string(want) {
		return fmt.Errorf("%w: write acknowledgement does not match request", ErrProtocol)
	}
	return nil
}
