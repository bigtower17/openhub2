package ezsp

import (
	"bytes"
	"fmt"
	"io"
	"time"
)

// ASH (Asynchronous Serial Host) protocol for Silicon Labs NCP.
// Reference: UG101 "UART Gateway Protocol Reference".

const flagByte = 0x7E

// Control byte types.
const (
	ctrlRST    = 0xC0
	ctrlRSTACK = 0xC1
	ctrlERROR  = 0xC2
)

// Conn manages an ASH connection over a serial port.
type Conn struct {
	rw      io.ReadWriter
	txSeq   uint8
	rxSeq   uint8
	ezspSeq uint8
	readBuf []byte
}

func NewConn(rw io.ReadWriter) *Conn {
	return &Conn{rw: rw}
}

// Reset sends ASH RST and waits for RSTACK.
func (c *Conn) Reset() error {
	c.txSeq = 0
	c.rxSeq = 0
	c.ezspSeq = 0
	c.readBuf = c.readBuf[:0]

	frame := encodeFrame([]byte{ctrlRST}, false)
	if _, err := c.rw.Write(frame); err != nil {
		return fmt.Errorf("write RST: %w", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := c.readRawFrame()
		if err != nil {
			return fmt.Errorf("read RSTACK: %w", err)
		}
		if len(raw) >= 1 && raw[0] == ctrlRSTACK {
			return nil
		}
	}
	return fmt.Errorf("no RSTACK received")
}

// Command sends an EZSP command and returns the response payload
// (everything after the frame ID byte).
func (c *Conn) Command(frameID byte, params ...byte) ([]byte, error) {
	ezsp := []byte{c.ezspSeq, 0x00, frameID}
	ezsp = append(ezsp, params...)
	c.ezspSeq++

	ctrl := (c.txSeq << 4) | (c.rxSeq & 0x07)
	data := append([]byte{ctrl}, randomize(ezsp)...)
	frame := encodeFrame(data, true)
	c.txSeq = (c.txSeq + 1) & 0x07

	if _, err := c.rw.Write(frame); err != nil {
		return nil, fmt.Errorf("write EZSP 0x%02X: %w", frameID, err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := c.readRawFrame()
		if err != nil {
			return nil, fmt.Errorf("read response for 0x%02X: %w", frameID, err)
		}
		if len(raw) < 1 {
			continue
		}
		ctrl := raw[0]
		if ctrl&0x80 == 0 && len(raw) > 1 {
			payload := derandomize(raw[1:])
			c.rxSeq = ((ctrl >> 4) + 1) & 0x07
			c.sendACK()
			if len(payload) >= 3 && payload[2] == frameID {
				return payload[3:], nil
			}
		}
	}
	return nil, fmt.Errorf("timeout waiting for EZSP 0x%02X response", frameID)
}

func (c *Conn) sendACK() {
	ctrl := byte(0x80) | (c.rxSeq & 0x07)
	frame := encodeFrame([]byte{ctrl}, true)
	c.rw.Write(frame)
}

func (c *Conn) readRawFrame() ([]byte, error) {
	for {
		if idx := bytes.IndexByte(c.readBuf, flagByte); idx >= 0 {
			raw := unescape(c.readBuf[:idx])
			c.readBuf = c.readBuf[idx+1:]
			if len(raw) < 3 {
				if len(raw) > 0 {
					continue
				}
				continue
			}
			body := raw[:len(raw)-2]
			gotCRC := uint16(raw[len(raw)-2])<<8 | uint16(raw[len(raw)-1])
			if crc16(body) != gotCRC {
				continue
			}
			return body, nil
		}
		tmp := make([]byte, 256)
		n, err := c.rw.Read(tmp)
		if n > 0 {
			c.readBuf = append(c.readBuf, tmp[:n]...)
		}
		if err != nil {
			return nil, err
		}
	}
}

// encodeFrame builds a wire frame: [data...] [CRC16] [0x7E], with byte stuffing.
// If withCRC is true, CRC is computed over data; otherwise data already includes CRC bytes.
func encodeFrame(data []byte, withCRC bool) []byte {
	var raw []byte
	raw = append(raw, data...)
	if withCRC {
		c := crc16(data)
		raw = append(raw, byte(c>>8), byte(c))
	}
	var out []byte
	out = append(out, escape(raw)...)
	out = append(out, flagByte)
	return out
}

func crc16(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func escape(data []byte) []byte {
	var out []byte
	for _, b := range data {
		switch b {
		case 0x7E, 0x7D, 0x11, 0x13, 0x18, 0x1A:
			out = append(out, 0x7D, b^0x20)
		default:
			out = append(out, b)
		}
	}
	return out
}

func unescape(data []byte) []byte {
	var out []byte
	for i := 0; i < len(data); i++ {
		if data[i] == 0x7D && i+1 < len(data) {
			out = append(out, data[i+1]^0x20)
			i++
		} else {
			out = append(out, data[i])
		}
	}
	return out
}

func randomize(data []byte) []byte {
	out := make([]byte, len(data))
	state := byte(0x42)
	for i, b := range data {
		if state&1 != 0 {
			state = (state >> 1) ^ 0xB8
		} else {
			state >>= 1
		}
		out[i] = b ^ state
	}
	return out
}

func derandomize(data []byte) []byte {
	return randomize(data)
}
