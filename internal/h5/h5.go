// Package h5 implements the HCI Three-wire UART transport (Bluetooth Core
// Vol 4 Part D), as spoken by the hub's Telink BLE controller: SLIP framing,
// no data integrity check, sliding window of one reliable packet.
package h5

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// Packet types.
const (
	TypeAck     = 0
	TypeCommand = 1
	TypeACL     = 2
	TypeSCO     = 3
	TypeEvent   = 4
	TypeISO     = 5
	TypeVendor  = 14
	TypeLink    = 15
)

var (
	msgSync    = []byte{0x01, 0x7e}
	msgSyncRsp = []byte{0x02, 0x7d}
	msgConf    = []byte{0x03, 0xfc}
	msgConfRsp = []byte{0x04, 0x7b}
)

// ErrPeerReset is returned by Send when the controller restarted its link.
var ErrPeerReset = errors.New("h5: controller reset the link")

// Packet is an HCI packet received from the controller.
type Packet struct {
	Type    uint8
	Payload []byte
}

const (
	stateUninit = iota
	stateInit
	stateActive
)

// Link is an H5 connection over a serial port.
type Link struct {
	rw  io.ReadWriter
	cfg byte // our config field: window size 1, no CRC, no OOF flow control

	wmu sync.Mutex // serializes wire writes

	mu      sync.Mutex
	state   int
	txSeq   uint8
	rxAck   uint8 // next sequence number expected from the controller
	waitSeq int   // sequence awaiting acknowledgement, -1 if none
	acked   chan struct{}
	active  chan struct{}
	reset   chan struct{}

	smu sync.Mutex // one outstanding reliable packet (window 1)

	// Packets delivers HCI packets from the controller, in order.
	Packets chan Packet
	// Debug, when set, receives a line per frame.
	Debug func(format string, args ...any)
}

func New(rw io.ReadWriter) *Link {
	return &Link{
		rw:      rw,
		cfg:     0x01,
		waitSeq: -1,
		acked:   make(chan struct{}, 1),
		active:  make(chan struct{}),
		reset:   make(chan struct{}),
		Packets: make(chan Packet, 64),
	}
}

// Run reads the serial port until it fails. Call it in its own goroutine.
func (l *Link) Run() error {
	buf := make([]byte, 512)
	var frame []byte
	in := false
	for {
		n, err := l.rw.Read(buf)
		for _, b := range buf[:n] {
			if b == 0xc0 {
				if in && len(frame) > 0 {
					l.handle(unslip(frame))
				}
				frame = frame[:0]
				in = true
				continue
			}
			if in {
				frame = append(frame, b)
			}
		}
		if err != nil {
			return err
		}
	}
}

// Establish performs SYNC/CONFIG until the link is active or timeout expires.
func (l *Link) Establish(timeout time.Duration) error {
	deadline := time.After(timeout)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		l.mu.Lock()
		st, active := l.state, l.active
		l.mu.Unlock()
		switch st {
		case stateActive:
			return nil
		case stateUninit:
			l.write(TypeLink, false, 0, msgSync)
		case stateInit:
			l.write(TypeLink, false, 0, append(msgConf[:2:2], l.cfg))
		}
		select {
		case <-active:
			return nil
		case <-tick.C:
		case <-deadline:
			return fmt.Errorf("h5: link not established (state %d)", st)
		}
	}
}

// Reset returns a channel closed when the controller restarts the link.
func (l *Link) Reset() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reset
}

// Send transmits a reliable HCI packet and waits for its acknowledgement.
func (l *Link) Send(typ uint8, payload []byte) error {
	l.smu.Lock()
	defer l.smu.Unlock()
	l.mu.Lock()
	if l.state != stateActive {
		l.mu.Unlock()
		return errors.New("h5: link not active")
	}
	seq := l.txSeq
	l.waitSeq = int(seq)
	reset := l.reset
	select {
	case <-l.acked:
	default:
	}
	l.mu.Unlock()

	for try := 0; try < 8; try++ {
		l.write(typ, true, seq, payload)
		select {
		case <-l.acked:
			return nil
		case <-reset:
			return ErrPeerReset
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("h5: no acknowledgement for packet type %d", typ)
}

func (l *Link) handle(f []byte) {
	if len(f) < 4 || byte(f[0]+f[1]+f[2]+f[3]) != 0xff {
		return
	}
	seq := f[0] & 7
	ack := f[0] >> 3 & 7
	reliable := f[0]&0x80 != 0
	typ := f[1] & 0x0f
	n := int(f[1]>>4) | int(f[2])<<4
	if len(f) < 4+n {
		return
	}
	payload := f[4 : 4+n]
	if l.Debug != nil {
		l.Debug("h5 rx seq=%d ack=%d rel=%v type=%d % x", seq, ack, reliable, typ, payload)
	}

	if typ == TypeLink {
		l.link(payload)
		return
	}

	l.mu.Lock()
	if l.state != stateActive {
		l.mu.Unlock()
		return
	}
	if l.waitSeq >= 0 && ack == uint8(l.waitSeq+1)&7 {
		l.txSeq = ack
		l.waitSeq = -1
		select {
		case l.acked <- struct{}{}:
		default:
		}
	}
	deliver := false
	if reliable {
		if seq == l.rxAck {
			l.rxAck = (l.rxAck + 1) & 7
			deliver = true
		}
	} else if typ != TypeAck {
		deliver = true
	}
	l.mu.Unlock()

	if reliable {
		l.write(TypeAck, false, 0, nil)
	}
	if deliver {
		p := Packet{Type: typ, Payload: append([]byte(nil), payload...)}
		select {
		case l.Packets <- p:
		default: // consumer stalled: drop rather than block the reader
		}
	}
}

func (l *Link) link(p []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case bytes.HasPrefix(p, msgSync):
		if l.state == stateActive {
			// The controller rebooted: start over.
			close(l.reset)
			l.reset = make(chan struct{})
			l.active = make(chan struct{})
			l.state = stateUninit
			l.txSeq, l.rxAck, l.waitSeq = 0, 0, -1
		}
		go l.write(TypeLink, false, 0, msgSyncRsp)
	case bytes.HasPrefix(p, msgSyncRsp):
		if l.state == stateUninit {
			l.state = stateInit
			go l.write(TypeLink, false, 0, append(msgConf[:2:2], l.cfg))
		}
	case bytes.HasPrefix(p, msgConf):
		go l.write(TypeLink, false, 0, append(msgConfRsp[:2:2], l.cfg))
	case bytes.HasPrefix(p, msgConfRsp):
		if l.state == stateInit {
			l.state = stateActive
			close(l.active)
		}
	}
}

func (l *Link) write(typ uint8, reliable bool, seq uint8, payload []byte) {
	l.mu.Lock()
	ack := l.rxAck
	l.mu.Unlock()
	frame := Encode(typ, reliable, seq, ack, payload)
	l.wmu.Lock()
	l.rw.Write(frame)
	l.wmu.Unlock()
	if l.Debug != nil {
		l.Debug("h5 tx seq=%d ack=%d rel=%v type=%d % x", seq, ack, reliable, typ, payload)
	}
}

// Encode builds a SLIP-framed H5 packet.
func Encode(typ uint8, reliable bool, seq, ack uint8, payload []byte) []byte {
	h := [4]byte{seq&7 | (ack&7)<<3, typ&0x0f | byte(len(payload)&0x0f)<<4, byte(len(payload) >> 4), 0}
	if reliable {
		h[0] |= 0x80
	}
	h[3] = ^(h[0] + h[1] + h[2])
	out := []byte{0xc0}
	out = slip(out, h[:])
	out = slip(out, payload)
	return append(out, 0xc0)
}

func slip(out, data []byte) []byte {
	for _, b := range data {
		switch b {
		case 0xc0:
			out = append(out, 0xdb, 0xdc)
		case 0xdb:
			out = append(out, 0xdb, 0xdd)
		default:
			out = append(out, b)
		}
	}
	return out
}

func unslip(data []byte) []byte {
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] == 0xdb && i+1 < len(data) {
			i++
			switch data[i] {
			case 0xdc:
				out = append(out, 0xc0)
			case 0xdd:
				out = append(out, 0xdb)
			default:
				out = append(out, data[i])
			}
			continue
		}
		out = append(out, data[i])
	}
	return out
}
