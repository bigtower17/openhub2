package ezsp

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"time"
)

// Additional EZSP frame IDs seen in host↔NCP traffic (EZSP v7).
const (
	frameStackStatusHandler      = 0x0019
	frameTrustCenterJoinHandler  = 0x0024
	frameIncomingMessageHandler  = 0x0045
	emberNetworkUp               = 0x90
	emberNetworkDown             = 0x91
	zdoDeviceAnnounce            = 0x0013
	zdoIEEEAddrRsp               = 0x8001
	zdoMgmtLqiRsp                = 0x8031
)

// Sniffer passively parses NCP→host EZSP frames so the network state can be
// reported while another host (ZHA) owns the radio. The bridge cannot query
// the NCP itself without resetting the host's session.
type Sniffer struct {
	mu      sync.Mutex
	net     NetworkInfo
	devices map[uint16]*Device
	names   map[uint16]string // short ID → EUI64
	buf     []byte
	v8      bool
	dirty   bool
	namesCh bool
}

func NewSniffer() *Sniffer {
	return &Sniffer{devices: map[uint16]*Device{}, names: map[uint16]string{}}
}

// Seed sets the coordinator info obtained from a direct scan.
func (s *Sniffer) Seed(n NetworkInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.net.EUI64 == "" {
		s.net = n
		s.dirty = true
	}
}

// WrapReader passes NCP→host data through unchanged while parsing it.
func (s *Sniffer) WrapReader(r io.Reader) io.Reader {
	return io.TeeReader(r, sniffWriter{s})
}

type sniffWriter struct{ s *Sniffer }

func (w sniffWriter) Write(p []byte) (int, error) {
	w.s.feed(p)
	return len(p), nil
}

// Reset clears the partial-frame buffer at the start of a new session.
func (s *Sniffer) Reset() {
	s.mu.Lock()
	s.buf = s.buf[:0]
	s.v8 = false
	s.mu.Unlock()
}

func (s *Sniffer) feed(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, data...)
	for {
		if i := bytes.LastIndexByte(s.buf, cancelByte); i >= 0 {
			s.buf = s.buf[i+1:]
		}
		idx := bytes.IndexByte(s.buf, flagByte)
		if idx < 0 {
			break
		}
		raw := unescape(s.buf[:idx])
		s.buf = s.buf[idx+1:]
		if len(raw) < 3 {
			continue
		}
		body := raw[:len(raw)-2]
		if crc16(body) != uint16(raw[len(raw)-2])<<8|uint16(raw[len(raw)-1]) {
			continue
		}
		s.parseFrame(body)
	}
	if len(s.buf) > 4096 {
		s.buf = s.buf[:0]
	}
}

func (s *Sniffer) parseFrame(frame []byte) {
	if len(frame) < 6 || frame[0]&0x80 != 0 {
		return
	}
	payload := derandomize(frame[1:])
	var frameID uint16
	var p []byte
	if s.v8 {
		frameID = uint16(payload[3]) | uint16(payload[4])<<8
		p = payload[5:]
	} else {
		frameID = uint16(payload[4])
		p = payload[5:]
	}

	switch frameID {
	case frameVersion:
		if len(p) >= 4 {
			s.v8 = p[0] >= 8
			s.net.EZSPVersion = p[0]
			v := binary.LittleEndian.Uint16(p[2:4])
			s.net.StackVersion = fmt.Sprintf("%d.%d.%d.%d", v>>12&0xF, v>>8&0xF, v>>4&0xF, v&0xF)
		}
	case frameGetEui64:
		if len(p) >= 8 {
			s.net.EUI64 = formatEUI64(p[:8])
		}
	case frameGetNodeID:
		if len(p) >= 2 {
			s.net.NodeID = binary.LittleEndian.Uint16(p[:2])
		}
	case frameNetworkState:
		if len(p) >= 1 {
			s.net.Status = networkStatus(p[0])
		}
	case frameStackStatusHandler:
		if len(p) >= 1 {
			switch p[0] {
			case emberNetworkUp:
				s.net.Status = "joined"
			case emberNetworkDown:
				s.net.Status = "no-network"
			}
		}
	case frameGetNetworkParameters:
		if len(p) >= 22 && p[0] == 0x00 {
			s.net.ExtPanID = formatEUI64(p[2:10])
			s.net.PanID = binary.LittleEndian.Uint16(p[10:12])
			s.net.Channel = p[13]
			if s.net.Status == "" || s.net.Status == "no-network" {
				s.net.Status = "joined"
			}
		}
	case frameTrustCenterJoinHandler:
		if len(p) >= 10 {
			s.learn(binary.LittleEndian.Uint16(p[0:2]), formatEUI64(p[2:10]))
		}
	case frameIncomingMessageHandler:
		s.incoming(p)
	default:
		return
	}
	s.dirty = true
}

// incomingMessageHandler (v7): type, apsFrame(11), lastHopLqi, lastHopRssi,
// sender(2), bindingIndex, addressIndex, messageLength, message.
func (s *Sniffer) incoming(p []byte) {
	if len(p) < 19 {
		return
	}
	profile := binary.LittleEndian.Uint16(p[1:3])
	cluster := binary.LittleEndian.Uint16(p[3:5])
	lqi := p[12]
	rssi := int8(p[13])
	sender := binary.LittleEndian.Uint16(p[14:16])
	n := int(p[18])
	msg := p[19:]
	if n < len(msg) {
		msg = msg[:n]
	}

	d := s.devices[sender]
	if d == nil {
		d = &Device{ShortID: sender}
		s.devices[sender] = d
	}
	d.LQI, d.RSSI = lqi, rssi
	d.LastSeen = time.Now().UTC().Format(time.RFC3339)

	if profile != 0x0000 {
		return
	}
	switch cluster {
	case zdoDeviceAnnounce: // tsn, nwk(2), ieee(8), cap
		if len(msg) >= 11 {
			s.learn(binary.LittleEndian.Uint16(msg[1:3]), formatEUI64(msg[3:11]))
		}
	case zdoIEEEAddrRsp: // tsn, status, ieee(8), nwk(2)
		if len(msg) >= 12 && msg[1] == 0 {
			s.learn(binary.LittleEndian.Uint16(msg[10:12]), formatEUI64(msg[2:10]))
		}
	case zdoMgmtLqiRsp: // tsn, status, total, start, count, entries[22]
		if len(msg) >= 5 && msg[1] == 0 {
			for i := 0; i < int(msg[4]); i++ {
				e := msg[5+i*22:]
				if len(e) < 22 {
					break
				}
				s.learn(binary.LittleEndian.Uint16(e[16:18]), formatEUI64(e[8:16]))
			}
		}
	}
}

func (s *Sniffer) learn(nwk uint16, eui string) {
	if nwk == s.net.NodeID || eui == s.net.EUI64 {
		return
	}
	if s.names[nwk] != eui {
		s.names[nwk] = eui
		s.namesCh = true
	}
	if d := s.devices[nwk]; d != nil {
		d.EUI64 = eui
	} else {
		s.devices[nwk] = &Device{ShortID: nwk, EUI64: eui}
	}
}

func networkStatus(b byte) string {
	switch b {
	case 0:
		return "no-network"
	case 1:
		return "joining"
	case 2:
		return "joined"
	case 3:
		return "joined-no-parent"
	case 4:
		return "leaving"
	}
	return fmt.Sprintf("unknown(%d)", b)
}

// Flush writes the current state to path if anything changed (or force).
func (s *Sniffer) Flush(path string, force bool) error {
	s.mu.Lock()
	if !s.dirty && !force {
		s.mu.Unlock()
		return nil
	}
	s.dirty = false
	r := ScanResult{Network: s.net, ScannedAt: time.Now().UTC().Format(time.RFC3339)}
	for nwk, d := range s.devices {
		if nwk == s.net.NodeID {
			continue
		}
		c := *d
		if c.EUI64 == "" {
			c.EUI64 = s.names[nwk]
		}
		r.Devices = append(r.Devices, c)
	}
	s.mu.Unlock()
	sort.Slice(r.Devices, func(i, j int) bool { return r.Devices[i].ShortID < r.Devices[j].ShortID })
	return os.WriteFile(path, r.JSON(), 0o644)
}

// LoadNames restores the short ID → EUI64 map learned in earlier runs.
func (s *Sniffer) LoadNames(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	m := map[string]string{}
	if json.Unmarshal(b, &m) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range m {
		var nwk uint16
		if _, err := fmt.Sscanf(k, "0x%04X", &nwk); err == nil {
			s.names[nwk] = v
		}
	}
}

// SaveNames persists the EUI64 map, only when it changed (it lives on flash).
func (s *Sniffer) SaveNames(path string) error {
	s.mu.Lock()
	if !s.namesCh {
		s.mu.Unlock()
		return nil
	}
	s.namesCh = false
	m := map[string]string{}
	for k, v := range s.names {
		m[fmt.Sprintf("0x%04X", k)] = v
	}
	s.mu.Unlock()
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(path, b, 0o644)
}
