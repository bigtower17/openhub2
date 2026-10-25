package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// The sampler keeps 24 hours of resource samples in memory (nothing is
// written to flash) for the dashboard charts.
const (
	sampleEvery = 15 * time.Second
	sampleKeep  = int(24 * time.Hour / sampleEvery)
)

type point struct {
	T   int64   `json:"t"`
	CPU float64 `json:"cpu"` // percent
	Mem int     `json:"mem"` // MB used
	RX  float64 `json:"rx"`  // bytes/s, all interfaces but lo
	TX  float64 `json:"tx"`  // bytes/s
	BLE float64 `json:"ble"` // advertisements per minute
}

type samplerState struct {
	mu     sync.Mutex
	points []point
	cpu    float64

	busy, total uint64
	rx, tx      uint64
	ads         int
	at          time.Time
}

var sampler = &samplerState{}

func (s *samplerState) lastCPU() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cpu
}

func (s *samplerState) run() {
	s.sample() // baseline
	for range time.Tick(sampleEvery) {
		s.sample()
	}
}

func (s *samplerState) sample() {
	now := time.Now()
	busy, total := cpuTimes()
	rx, tx := netBytes()
	ads := bleAdverts()
	mem := getResources().MemUsedMB

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.at.IsZero() {
		dt := now.Sub(s.at).Seconds()
		p := point{T: now.Unix(), Mem: mem}
		if total > s.total {
			p.CPU = float64(busy-s.busy) * 100 / float64(total-s.total)
		}
		if rx >= s.rx && tx >= s.tx {
			p.RX, p.TX = float64(rx-s.rx)/dt, float64(tx-s.tx)/dt
		}
		if ads >= s.ads {
			p.BLE = float64(ads-s.ads) * 60 / dt
		}
		s.cpu = p.CPU
		s.points = append(s.points, p)
		if len(s.points) > sampleKeep {
			s.points = s.points[len(s.points)-sampleKeep:]
		}
	}
	s.busy, s.total, s.rx, s.tx, s.ads, s.at = busy, total, rx, tx, ads, now
}

// history returns the samples of the last d, averaged down to at most max points.
func (s *samplerState) history(d time.Duration, max int) []point {
	s.mu.Lock()
	cutoff := time.Now().Add(-d).Unix()
	var pts []point
	for _, p := range s.points {
		if p.T >= cutoff {
			pts = append(pts, p)
		}
	}
	s.mu.Unlock()
	if len(pts) <= max {
		return pts
	}
	step := (len(pts) + max - 1) / max
	var out []point
	for i := 0; i < len(pts); i += step {
		end := min(i+step, len(pts))
		var a point
		for _, p := range pts[i:end] {
			a.CPU += p.CPU
			a.Mem += p.Mem
			a.RX += p.RX
			a.TX += p.TX
			a.BLE += p.BLE
		}
		n := float64(end - i)
		a.T = pts[end-1].T
		a.CPU, a.RX, a.TX, a.BLE = a.CPU/n, a.RX/n, a.TX/n, a.BLE/n
		a.Mem /= end - i
		out = append(out, a)
	}
	return out
}

func cpuTimes() (busy, total uint64) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return
	}
	f := strings.Fields(strings.SplitN(string(b), "\n", 2)[0])
	for i, v := range f[1:] {
		var n uint64
		fmt.Sscanf(v, "%d", &n)
		total += n
		if i != 3 && i != 4 { // idle, iowait
			busy += n
		}
	}
	return
}

func netBytes() (rx, tx uint64) {
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n")[2:] {
		name, rest, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) == "lo" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		var r, t uint64
		fmt.Sscanf(f[0], "%d", &r)
		fmt.Sscanf(f[8], "%d", &t)
		rx += r
		tx += t
	}
	return
}

func bleAdverts() int {
	b, err := os.ReadFile("/tmp/btproxy.json")
	if err != nil {
		return 0
	}
	var v struct{ Advertisements int }
	json.Unmarshal(b, &v)
	return v.Advertisements
}
