// Package hci drives a Bluetooth LE controller over an H5 link: controller
// setup and passive/active LE scanning.
package hci

import (
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/bigtower17/openhub2/internal/h5"
)

const (
	opReset            = 0x0c03
	opSetEventMask     = 0x0c01
	opReadLocalVersion = 0x1001
	opReadBDAddr       = 0x1009
	opLESetEventMask   = 0x2001
	opLESetScanParams  = 0x200b
	opLESetScanEnable  = 0x200c
	evtCommandComplete = 0x0e
	evtCommandStatus   = 0x0f
	evtLEMeta          = 0x3e
	subLEAdvReport     = 0x02
)

// Advertisement is one LE advertising or scan response report.
type Advertisement struct {
	Address     [6]byte // most significant byte first
	AddressType uint8   // 0 public, 1 random
	EventType   uint8
	RSSI        int8
	Data        []byte
}

// Version is the controller's Read Local Version Information reply.
type Version struct {
	HCIVersion, LMPVersion uint8
	HCIRevision, LMPSub    uint16
	Manufacturer           uint16
}

// Controller is an HCI controller reachable through an H5 link.
type Controller struct {
	link *h5.Link

	cmu     sync.Mutex // one command in flight
	mu      sync.Mutex
	pending uint16
	reply   chan []byte

	// Adverts receives advertising reports while scanning.
	Adverts chan Advertisement
}

func New(link *h5.Link) *Controller {
	c := &Controller{link: link, reply: make(chan []byte, 1), Adverts: make(chan Advertisement, 256)}
	go c.dispatch()
	return c
}

func (c *Controller) dispatch() {
	for p := range c.link.Packets {
		if p.Type != h5.TypeEvent || len(p.Payload) < 2 {
			continue
		}
		code, params := p.Payload[0], p.Payload[2:]
		switch code {
		case evtCommandComplete:
			if len(params) >= 3 {
				c.complete(binary.LittleEndian.Uint16(params[1:3]), params[3:])
			}
		case evtCommandStatus:
			if len(params) >= 4 {
				c.complete(binary.LittleEndian.Uint16(params[2:4]), params[:1])
			}
		case evtLEMeta:
			if len(params) > 0 {
				c.leMeta(params[0], params[1:])
			}
		}
	}
}

func (c *Controller) complete(op uint16, ret []byte) {
	c.mu.Lock()
	match := op == c.pending
	if match {
		c.pending = 0
	}
	c.mu.Unlock()
	if match {
		select {
		case c.reply <- append([]byte(nil), ret...):
		default:
		}
	}
}

// Command sends an HCI command and returns its return parameters
// (Command Complete) or status byte (Command Status).
func (c *Controller) Command(op uint16, params ...byte) ([]byte, error) {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	c.mu.Lock()
	c.pending = op
	select {
	case <-c.reply:
	default:
	}
	c.mu.Unlock()

	pkt := append([]byte{byte(op), byte(op >> 8), byte(len(params))}, params...)
	if err := c.link.Send(h5.TypeCommand, pkt); err != nil {
		return nil, err
	}
	select {
	case r := <-c.reply:
		return r, nil
	case <-time.After(2 * time.Second):
		return nil, fmt.Errorf("hci: no reply to command 0x%04x", op)
	}
}

func (c *Controller) check(op uint16, params ...byte) ([]byte, error) {
	r, err := c.Command(op, params...)
	if err != nil {
		return nil, err
	}
	if len(r) < 1 || r[0] != 0 {
		return r, fmt.Errorf("hci: command 0x%04x failed: status % x", op, r)
	}
	return r[1:], nil
}

// Init resets the controller and enables LE meta events. It returns the
// controller version and public address (most significant byte first).
func (c *Controller) Init() (Version, [6]byte, error) {
	var v Version
	var addr [6]byte
	if _, err := c.check(opReset); err != nil {
		return v, addr, err
	}
	r, err := c.check(opReadLocalVersion)
	if err != nil {
		return v, addr, err
	}
	if len(r) >= 8 {
		v = Version{r[0], r[3], binary.LittleEndian.Uint16(r[1:3]), binary.LittleEndian.Uint16(r[6:8]), binary.LittleEndian.Uint16(r[4:6])}
	}
	r, err = c.check(opReadBDAddr)
	if err != nil {
		return v, addr, err
	}
	if len(r) >= 6 {
		for i := 0; i < 6; i++ {
			addr[i] = r[5-i]
		}
	}
	// default mask plus LE Meta (bit 61)
	if _, err := c.check(opSetEventMask, 0xff, 0xff, 0xfb, 0xff, 0x07, 0xf8, 0xbf, 0x3d); err != nil {
		return v, addr, err
	}
	if _, err := c.check(opLESetEventMask, 0x1f, 0, 0, 0, 0, 0, 0, 0); err != nil {
		return v, addr, err
	}
	return v, addr, nil
}

// Scan starts LE scanning (active sends scan requests) without duplicate
// filtering, so every advertisement reaches the caller.
func (c *Controller) Scan(active bool) error {
	c.Command(opLESetScanEnable, 0, 0) // ignore "already disabled"
	typ := byte(0)
	if active {
		typ = 1
	}
	// 60 ms interval and window: listen continuously.
	if _, err := c.check(opLESetScanParams, typ, 0x60, 0x00, 0x60, 0x00, 0x00, 0x00); err != nil {
		return err
	}
	_, err := c.check(opLESetScanEnable, 1, 0)
	return err
}

// StopScan disables LE scanning.
func (c *Controller) StopScan() error {
	_, err := c.check(opLESetScanEnable, 0, 0)
	return err
}

func (c *Controller) leMeta(sub byte, p []byte) {
	if sub != subLEAdvReport || len(p) < 1 {
		return
	}
	// Reports are laid out one after another, as Linux parses them.
	n, p := int(p[0]), p[1:]
	for i := 0; i < n && len(p) >= 9; i++ {
		var a Advertisement
		a.EventType, a.AddressType = p[0], p[1]
		for j := 0; j < 6; j++ {
			a.Address[j] = p[7-j]
		}
		l := int(p[8])
		if len(p) < 10+l {
			return
		}
		a.Data = append([]byte(nil), p[9:9+l]...)
		a.RSSI = int8(p[9+l])
		p = p[10+l:]
		select {
		case c.Adverts <- a:
		default:
		}
	}
}
