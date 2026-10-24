package ezsp

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"time"
)

// ASH (Asynchronous Serial Host) protocol for Silicon Labs NCP.
// Reference: UG101 "UART Gateway Protocol Reference".

const flagByte = 0x7E

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
	EzspV8  bool // extended frame format (2-byte fc + 2-byte frame ID)
	readBuf []byte
	debug   bool
}

func NewConn(rw io.ReadWriter) *Conn {
	return &Conn{rw: rw}
}

func (c *Conn) Reset() error {
	c.txSeq = 0
	c.rxSeq = 0
	c.ezspSeq = 0
	c.readBuf = c.readBuf[:0]

	rst := append([]byte{0x1A}, encodeFrame([]byte{ctrlRST}, true)...)
	if c.debug {
		log.Printf("ash: tx RST % x", rst)
	}
	if _, err := c.rw.Write(rst); err != nil {
		return fmt.Errorf("write RST: %w", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := c.readRawFrame(deadline)
		if err != nil {
			return fmt.Errorf("read RSTACK: %w", err)
		}
		if c.debug {
			log.Printf("ash: rx frame % x", raw)
		}
		if len(raw) >= 1 && raw[0] == ctrlRSTACK {
			time.Sleep(500 * time.Millisecond)
			// Drain any data the NCP sent after RSTACK.
			c.readBuf = c.readBuf[:0]
			tmp := make([]byte, 256)
			n, _ := c.rw.Read(tmp)
			if n > 0 && c.debug {
				log.Printf("ash: drained %d bytes after RSTACK: % x", n, tmp[:n])
			}
			return nil
		}
	}
	return fmt.Errorf("no RSTACK received")
}

// Command sends an EZSP command and returns the response parameters
// (payload after the EZSP header). frameID is uint16 for v8 compatibility.
func (c *Conn) Command(frameID uint16, params ...byte) ([]byte, error) {
	if c.debug && len(c.readBuf) > 0 {
		log.Printf("ash: readBuf before cmd 0x%04X: % x", frameID, c.readBuf)
	}

	var ezsp []byte
	if c.EzspV8 {
		ezsp = []byte{c.ezspSeq, 0x00, 0x01, byte(frameID), byte(frameID >> 8)}
	} else {
		ezsp = []byte{c.ezspSeq, 0x00, 0xFF, 0x00, byte(frameID)}
	}
	ezsp = append(ezsp, params...)
	c.ezspSeq++

	ctrl := (c.txSeq << 4) | (c.rxSeq & 0x07)
	data := append([]byte{ctrl}, randomize(ezsp)...)
	frame := encodeFrame(data, true)
	c.txSeq = (c.txSeq + 1) & 0x07

	if c.debug {
		log.Printf("ash: tx DATA seq=%d ack=%d ezsp=0x%04X wire=% x", c.txSeq-1, c.rxSeq, frameID, frame)
	}
	if _, err := c.rw.Write(frame); err != nil {
		return nil, fmt.Errorf("write EZSP 0x%04X: %w", frameID, err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := c.readRawFrame(deadline)
		if err != nil {
			return nil, fmt.Errorf("read response for 0x%04X: %w", frameID, err)
		}
		if len(raw) < 1 {
			continue
		}
		if c.debug {
			log.Printf("ash: rx ctrl=0x%02X body=% x", raw[0], raw[1:])
		}
		ashCtrl := raw[0]
		if ashCtrl&0x80 != 0 {
			continue
		}
		if len(raw) <= 1 {
			continue
		}
		payload := derandomize(raw[1:])
		c.rxSeq = ((ashCtrl >> 4) + 1) & 0x07
		c.sendACK()
		if c.debug {
			log.Printf("ash: decoded EZSP (v8=%v): % x", c.EzspV8, payload)
		}

		if c.EzspV8 {
			if len(payload) < 5 {
				if c.debug {
					log.Printf("ash: v8 frame too short (%d bytes)", len(payload))
				}
				continue
			}
			respID := uint16(payload[3]) | uint16(payload[4])<<8
			if c.debug {
				log.Printf("ash: v8 resp: seq=%d fc=%02x%02x id=0x%04X params=% x",
					payload[0], payload[1], payload[2], respID, payload[5:])
			}
			return payload[5:], nil
		}

		if len(payload) < 5 {
			if c.debug {
				log.Printf("ash: v5 frame too short (%d bytes)", len(payload))
			}
			continue
		}
		if c.debug {
			log.Printf("ash: v5 resp: seq=%d fc=%02x/%02x/%02x id=0x%02X params=% x",
				payload[0], payload[1], payload[2], payload[3], payload[4], payload[5:])
		}
		return payload[5:], nil
	}
	return nil, fmt.Errorf("timeout waiting for EZSP 0x%04X response", frameID)
}

func (c *Conn) sendACK() {
	ctrl := byte(0x80) | (c.rxSeq & 0x07)
	frame := encodeFrame([]byte{ctrl}, true)
	c.rw.Write(frame)
}

const cancelByte = 0x1A

func (c *Conn) readRawFrame(deadline time.Time) ([]byte, error) {
	for time.Now().Before(deadline) {
		if idx := bytes.LastIndexByte(c.readBuf, cancelByte); idx >= 0 {
			c.readBuf = c.readBuf[idx+1:]
		}
		if idx := bytes.IndexByte(c.readBuf, flagByte); idx >= 0 {
			segment := c.readBuf[:idx]
			c.readBuf = c.readBuf[idx+1:]
			if c.debug {
				log.Printf("ash: wire segment: % x", segment)
			}
			raw := unescape(segment)
			if len(raw) < 3 {
				continue
			}
			if frame := findValidFrame(raw); frame != nil {
				return frame, nil
			}
			if c.debug {
				log.Printf("ash: no valid CRC in segment: % x", raw)
			}
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
	return nil, fmt.Errorf("read timeout")
}

// findValidFrame scans raw bytes (unescaped, between flags) for a sub-frame
// with valid CRC. Tries from right to left so the most recent frame wins.
func findValidFrame(raw []byte) []byte {
	if len(raw) >= 3 {
		body := raw[:len(raw)-2]
		gotCRC := uint16(raw[len(raw)-2])<<8 | uint16(raw[len(raw)-1])
		if crc16(body) == gotCRC {
			return body
		}
	}
	for i := len(raw) - 3; i >= 1; i-- {
		sub := raw[i:]
		if len(sub) < 3 {
			continue
		}
		body := sub[:len(sub)-2]
		gotCRC := uint16(sub[len(sub)-2])<<8 | uint16(sub[len(sub)-1])
		if crc16(body) == gotCRC {
			return body
		}
	}
	return nil
}

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
		out[i] = b ^ state
		if state&1 != 0 {
			state = (state >> 1) ^ 0xB8
		} else {
			state >>= 1
		}
	}
	return out
}

func derandomize(data []byte) []byte {
	return randomize(data)
}
