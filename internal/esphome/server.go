// Package esphome implements the subset of the ESPHome native API (plaintext)
// that a Bluetooth proxy needs, so Home Assistant adopts the hub as one.
package esphome

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
)

// Message type IDs from esphome/components/api/api.proto.
const (
	msgHelloRequest            = 1
	msgHelloResponse           = 2
	msgAuthRequest             = 3
	msgAuthResponse            = 4
	msgDisconnectRequest       = 5
	msgDisconnectResponse      = 6
	msgPingRequest             = 7
	msgPingResponse            = 8
	msgDeviceInfoRequest       = 9
	msgDeviceInfoResponse      = 10
	msgListEntitiesRequest     = 11
	msgListEntitiesDone        = 19
	msgSubscribeAdverts        = 66
	msgConnectionsFreeRequest  = 80
	msgConnectionsFreeResponse = 81
	msgUnsubscribeAdverts      = 87
	msgRawAdverts              = 93
	msgScannerState            = 126
	msgScannerSetMode          = 127
)

// Bluetooth proxy feature flags (aioesphomeapi BluetoothProxyFeature).
const (
	featurePassiveScan    = 1 << 0
	featureRawAdvertising = 1 << 5
	featureStateAndMode   = 1 << 6
	scannerStateRunning   = 2
	scannerStateFailed    = 3
	apiVersionMajor       = 1
	apiVersionMinor       = 10
	maxAdvertsPerMessage  = 16
	clientQueue           = 64
)

// Info describes the device to Home Assistant.
type Info struct {
	Name           string // hostname-style node name
	FriendlyName   string
	MAC            string // network MAC, AA:BB:CC:DD:EE:FF
	BluetoothMAC   string
	Model          string
	Manufacturer   string
	Version        string // reported as esphome_version
	ProjectName    string
	ProjectVersion string
	WebServerPort  int
}

// Advertisement is a raw LE advertisement forwarded to Home Assistant.
type Advertisement struct {
	Address     [6]byte // most significant byte first
	AddressType uint8
	RSSI        int8
	Data        []byte
}

// Server accepts Home Assistant connections and streams advertisements.
type Server struct {
	Info Info
	// SetMode is called when Home Assistant asks for active (true) or
	// passive scanning. It returns false if the scanner failed.
	SetMode func(active bool) bool

	mu      sync.Mutex
	clients map[*client]struct{}
	active  bool
	running bool
}

type client struct {
	conn       net.Conn
	out        chan []byte
	subscribed bool
}

func (s *Server) Serve(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.handle(c)
	}
}

// SetScannerState records whether scanning runs, for state reports.
func (s *Server) SetScannerState(running, active bool) {
	s.mu.Lock()
	s.running, s.active = running, active
	clients := s.subscribedLocked()
	s.mu.Unlock()
	msg := s.scannerState()
	for _, c := range clients {
		c.send(msg)
	}
}

// Publish sends advertisements to every subscribed client.
func (s *Server) Publish(ads []Advertisement) {
	s.mu.Lock()
	clients := s.subscribedLocked()
	s.mu.Unlock()
	if len(clients) == 0 {
		return
	}
	for len(ads) > 0 {
		n := min(len(ads), maxAdvertsPerMessage)
		var m pb
		for _, a := range ads[:n] {
			var e pb
			addr := binary.BigEndian.Uint64(append([]byte{0, 0}, a.Address[:]...))
			e = binary.AppendUvarint(e.key(1, 0), addr)
			e = e.sint(2, int64(a.RSSI))
			e = e.uint(3, uint64(a.AddressType))
			e = e.bytes(4, a.Data)
			m = m.bytes(1, e)
		}
		f := frame(msgRawAdverts, m)
		for _, c := range clients {
			c.send(f)
		}
		ads = ads[n:]
	}
}

// Clients returns the number of connected Home Assistant sessions.
func (s *Server) Clients() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

func (s *Server) subscribedLocked() []*client {
	var out []*client
	for c := range s.clients {
		if c.subscribed {
			out = append(out, c)
		}
	}
	return out
}

func (c *client) send(f []byte) {
	select {
	case c.out <- f:
	default: // slow client: drop advertisements rather than stall the scanner
	}
}

func (s *Server) handle(conn net.Conn) {
	c := &client{conn: conn, out: make(chan []byte, clientQueue)}
	s.mu.Lock()
	if s.clients == nil {
		s.clients = map[*client]struct{}{}
	}
	s.clients[c] = struct{}{}
	s.mu.Unlock()
	log.Printf("api: %s connected", conn.RemoteAddr())

	done := make(chan struct{})
	go func() {
		for {
			select {
			case f := <-c.out:
				if _, err := conn.Write(f); err != nil {
					conn.Close()
					return
				}
			case <-done:
				return
			}
		}
	}()
	err := s.read(c)
	close(done)
	conn.Close()
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
	log.Printf("api: %s disconnected: %v", conn.RemoteAddr(), err)
}

func (s *Server) read(c *client) error {
	r := bufio.NewReader(c.conn)
	for {
		pre, err := r.ReadByte()
		if err != nil {
			return err
		}
		if pre != 0 {
			return fmt.Errorf("encrypted API connection (preamble 0x%02x) not supported; remove the encryption key in Home Assistant", pre)
		}
		n, err := binary.ReadUvarint(r)
		if err != nil {
			return err
		}
		typ, err := binary.ReadUvarint(r)
		if err != nil {
			return err
		}
		if n > 4096 {
			return fmt.Errorf("message too large (%d bytes)", n)
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			return err
		}
		if stop := s.dispatch(c, int(typ), payload); stop {
			return nil
		}
	}
}

func (s *Server) dispatch(c *client, typ int, payload []byte) (stop bool) {
	switch typ {
	case msgHelloRequest:
		c.send(frame(msgHelloResponse, pb(nil).
			uint(1, apiVersionMajor).uint(2, apiVersionMinor).
			str(3, "openhub2 "+s.Info.ProjectVersion).str(4, s.Info.Name)))
	case msgAuthRequest:
		c.send(frame(msgAuthResponse, nil))
	case msgDisconnectRequest:
		c.send(frame(msgDisconnectResponse, nil))
		return true
	case msgDisconnectResponse:
		return true
	case msgPingRequest:
		c.send(frame(msgPingResponse, nil))
	case msgDeviceInfoRequest:
		i := s.Info
		c.send(frame(msgDeviceInfoResponse, pb(nil).
			str(2, i.Name).str(3, i.MAC).str(4, i.Version).
			str(6, i.Model).str(8, i.ProjectName).str(9, i.ProjectVersion).
			uint(10, uint64(i.WebServerPort)).
			str(12, i.Manufacturer).str(13, i.FriendlyName).
			uint(15, featurePassiveScan|featureRawAdvertising|featureStateAndMode).
			str(18, i.BluetoothMAC)))
	case msgListEntitiesRequest:
		c.send(frame(msgListEntitiesDone, nil))
	case msgSubscribeAdverts:
		s.mu.Lock()
		c.subscribed = true
		s.mu.Unlock()
		c.send(s.scannerState())
	case msgUnsubscribeAdverts:
		s.mu.Lock()
		c.subscribed = false
		s.mu.Unlock()
	case msgConnectionsFreeRequest:
		c.send(frame(msgConnectionsFreeResponse, nil))
	case msgScannerSetMode:
		f, err := fields(payload)
		if err == nil && s.SetMode != nil {
			active := f[1] == 1
			ok := s.SetMode(active)
			s.SetScannerState(ok, active)
		}
	}
	return false
}

func (s *Server) scannerState() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := uint64(scannerStateFailed)
	if s.running {
		state = scannerStateRunning
	}
	mode := uint64(0)
	if s.active {
		mode = 1
	}
	return frame(msgScannerState, pb(nil).uint(1, state).uint(2, mode).uint(3, mode))
}
