package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/bigtower17/openhub2/internal/esphome"
	"github.com/bigtower17/openhub2/internal/hci"
)

const devicesFile = "/tmp/btproxy-devices.json"

type device struct {
	Address      string `json:"address"`
	AddressType  uint8  `json:"address_type"`
	Name         string `json:"name,omitempty"`
	RSSI         int8   `json:"rssi"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Services     string `json:"services,omitempty"`
	LastSeen     string `json:"last_seen"`
	last         time.Time
}

type stats struct {
	mu    sync.Mutex
	count int
	seen  map[[6]byte]*device
}

// add records an advertisement, keeping fields learned from earlier ones
// (names often arrive only in scan responses).
func (s *stats) add(a hci.Advertisement) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count++
	d := s.seen[a.Address]
	if d == nil {
		d = &device{Address: formatMAC(a.Address[:]), AddressType: a.AddressType}
		s.seen[a.Address] = d
	}
	d.RSSI, d.last = a.RSSI, time.Now()
	name, mfr, svc := hci.ParseAD(a.Data)
	if name != "" {
		d.Name = name
	}
	if mfr != "" {
		d.Manufacturer = mfr
	}
	if svc != "" {
		d.Services = svc
	}
}

// report writes status and device files for the web panel every 10 seconds.
func (s *stats) report(srv *esphome.Server, btMAC string, ver hci.Version) {
	start := time.Now()
	for range time.Tick(10 * time.Second) {
		now := time.Now()
		s.mu.Lock()
		recent := 0
		var list []device
		for a, d := range s.seen {
			age := now.Sub(d.last)
			if age > 10*time.Minute {
				delete(s.seen, a)
				continue
			}
			if age <= 5*time.Minute {
				recent++
			}
			c := *d
			c.LastSeen = d.last.UTC().Format(time.RFC3339)
			list = append(list, c)
		}
		count := s.count
		s.mu.Unlock()

		sort.Slice(list, func(i, j int) bool { return list[i].last.After(list[j].last) })
		if len(list) > 200 {
			list = list[:200]
		}
		writeJSON(devicesFile, list)
		writeJSON(statusFile, map[string]any{
			"bluetooth_mac":  btMAC,
			"controller":     fmt.Sprintf("HCI %d rev 0x%04x, manufacturer 0x%04x", ver.HCIVersion, ver.HCIRevision, ver.Manufacturer),
			"name":           srv.Info.Name,
			"clients":        srv.Clients(),
			"advertisements": count,
			"devices_5min":   recent,
			"uptime_seconds": int(now.Sub(start).Seconds()),
			"updated_at":     now.UTC().Format(time.RFC3339),
		})
	}
}

func writeJSON(path string, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		log.Printf("status: %v", err)
		return
	}
	os.Rename(tmp, path)
}
