// btproxy turns the hub's Bluetooth LE radio (Telink controller, HCI over
// H5 on /dev/ttyS2) into an ESPHome Bluetooth proxy for Home Assistant.
//
// The stock BLE daemon (miio_bt) must not be running: only one program can
// own the UART.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/bigtower17/openhub2/internal/esphome"
	"github.com/bigtower17/openhub2/internal/h5"
	"github.com/bigtower17/openhub2/internal/hci"
	"github.com/bigtower17/openhub2/internal/serial"
)

const (
	statusFile  = "/tmp/btproxy.json"
	startMarker = "/data/openhub2/btproxy.starting"
)

var version = "dev"

var (
	port     = flag.String("port", "/dev/ttyS2", "BLE controller serial port")
	baud     = flag.Int("baud", 1000000, "BLE controller baud rate")
	resetPin = flag.Int("reset-gpio", 60, "GPIO driving the controller reset line (active low), -1 to disable")
	addr     = flag.String("addr", ":6053", "ESPHome API listen address")
	iface    = flag.String("iface", "wlan0", "network interface for mDNS and the reported MAC")
	probe    = flag.Duration("probe", 0, "initialize the controller, scan for this long, print what was seen and exit")
	debug    = flag.Bool("debug", false, "log every H5 frame")
)

func main() {
	flag.Parse()
	// post_init.sh skips btproxy at boot if this marker survived a crash.
	os.WriteFile(startMarker, nil, 0o644)
	exec.Command("sync").Run()
	if *resetPin >= 0 {
		if err := resetController(*resetPin); err != nil {
			log.Printf("reset gpio: %v", err)
		}
	}
	ser, err := serial.Open(*port, *baud, false)
	if err != nil {
		log.Fatal(err)
	}
	link := h5.New(ser)
	if *debug {
		link.Debug = log.Printf
	}
	go func() { log.Fatalf("serial: %v", link.Run()) }()
	if err := link.Establish(5 * time.Second); err != nil {
		log.Fatal(err)
	}
	ctrl := hci.New(link)
	ver, bdaddr, err := ctrl.Init()
	os.Remove(startMarker)
	if err != nil {
		log.Fatalf("controller init: %v", err)
	}
	btMAC := formatMAC(bdaddr[:])
	log.Printf("controller: HCI %d rev 0x%04x, LMP %d sub 0x%04x, manufacturer 0x%04x, address %s",
		ver.HCIVersion, ver.HCIRevision, ver.LMPVersion, ver.LMPSub, ver.Manufacturer, btMAC)

	if *probe > 0 {
		os.WriteFile(startMarker, nil, 0o644) // a probe can still take the hub down while scanning
		runProbe(ctrl, *probe)
		os.Remove(startMarker)
		return
	}

	mac := netMAC(*iface)
	suffix := strings.ToLower(strings.ReplaceAll(mac, ":", ""))
	if len(suffix) > 6 {
		suffix = suffix[len(suffix)-6:]
	}
	srv := &esphome.Server{Info: esphome.Info{
		Name:           "openhub2-" + suffix,
		FriendlyName:   "OpenHub2 BT proxy " + strings.ToUpper(suffix[max(0, len(suffix)-4):]),
		MAC:            mac,
		BluetoothMAC:   btMAC,
		Model:          "Xiaomi Smart Home Hub 2",
		Manufacturer:   "Xiaomi",
		Version:        "2025.10.0",
		ProjectName:    "openhub2.btproxy",
		ProjectVersion: version,
		WebServerPort:  80,
	}}

	var smu sync.Mutex
	srv.SetMode = func(active bool) bool {
		smu.Lock()
		defer smu.Unlock()
		if err := ctrl.Scan(active); err != nil {
			log.Printf("scan mode: %v", err)
			return false
		}
		log.Printf("scan mode: active=%v", active)
		return true
	}
	if err := ctrl.Scan(false); err != nil {
		log.Fatalf("scan: %v", err)
	}
	srv.SetScannerState(true, false)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	go func() { log.Fatalf("api: %v", srv.Serve(ln)) }()
	go func() {
		if err := srv.Advertise(*iface, ln.Addr().(*net.TCPAddr).Port); err != nil {
			log.Printf("mdns: %v (add the proxy in Home Assistant by IP, port 6053)", err)
		}
	}()
	log.Printf("ESPHome API on %s as %s", *addr, srv.Info.Name)

	st := &stats{seen: map[[6]byte]time.Time{}}
	go st.report(srv, btMAC, ver)

	flush := time.NewTicker(100 * time.Millisecond)
	defer flush.Stop()
	var batch []esphome.Advertisement
	for {
		select {
		case a := <-ctrl.Adverts:
			st.add(a.Address)
			batch = append(batch, esphome.Advertisement{Address: a.Address, AddressType: a.AddressType, RSSI: a.RSSI, Data: a.Data})
		case <-flush.C:
			if len(batch) > 0 {
				srv.Publish(batch)
				batch = batch[:0]
			}
		case <-link.Reset():
			log.Fatal("controller reset the H5 link, restarting")
		}
	}
}

func runProbe(ctrl *hci.Controller, d time.Duration) {
	if err := ctrl.Scan(false); err != nil {
		log.Fatalf("scan: %v", err)
	}
	seen := map[[6]byte]int8{}
	n := 0
	end := time.After(d)
	for {
		select {
		case a := <-ctrl.Adverts:
			n++
			seen[a.Address] = a.RSSI
		case <-end:
			ctrl.StopScan()
			fmt.Printf("%d advertisements from %d devices\n", n, len(seen))
			for a, rssi := range seen {
				fmt.Printf("  %s %d dBm\n", formatMAC(a[:]), rssi)
			}
			return
		}
	}
}

type stats struct {
	mu    sync.Mutex
	count int
	seen  map[[6]byte]time.Time
}

func (s *stats) add(a [6]byte) {
	s.mu.Lock()
	s.count++
	s.seen[a] = time.Now()
	s.mu.Unlock()
}

// report writes a status file for the web panel every 10 seconds.
func (s *stats) report(srv *esphome.Server, btMAC string, ver hci.Version) {
	start := time.Now()
	for range time.Tick(10 * time.Second) {
		s.mu.Lock()
		cutoff := time.Now().Add(-5 * time.Minute)
		for a, t := range s.seen {
			if t.Before(cutoff) {
				delete(s.seen, a)
			}
		}
		st := map[string]any{
			"bluetooth_mac":  btMAC,
			"controller":     fmt.Sprintf("HCI %d rev 0x%04x, manufacturer 0x%04x", ver.HCIVersion, ver.HCIRevision, ver.Manufacturer),
			"name":           srv.Info.Name,
			"clients":        srv.Clients(),
			"advertisements": s.count,
			"devices_5min":   len(s.seen),
			"uptime_seconds": int(time.Since(start).Seconds()),
			"updated_at":     time.Now().UTC().Format(time.RFC3339),
		}
		s.mu.Unlock()
		b, _ := json.MarshalIndent(st, "", "  ")
		if err := os.WriteFile(statusFile, b, 0o644); err != nil {
			log.Printf("status: %v", err)
		}
	}
}

// resetController pulses the controller's reset line low, as miio_bt does.
func resetController(pin int) error {
	path := fmt.Sprintf("/sys/class/gpio/gpio%d", pin)
	if _, err := os.Stat(path); err != nil {
		if err := os.WriteFile("/sys/class/gpio/export", []byte(fmt.Sprint(pin)), 0o200); err != nil {
			return err
		}
	}
	if err := os.WriteFile(path+"/direction", []byte("out"), 0o644); err != nil {
		return err
	}
	for _, step := range []struct {
		v string
		d time.Duration
	}{{"1", 10 * time.Millisecond}, {"0", 50 * time.Millisecond}, {"1", 500 * time.Millisecond}} {
		if err := os.WriteFile(path+"/value", []byte(step.v), 0o644); err != nil {
			return err
		}
		time.Sleep(step.d)
	}
	return nil
}

func netMAC(name string) string {
	if ifi, err := net.InterfaceByName(name); err == nil && len(ifi.HardwareAddr) == 6 {
		return strings.ToUpper(ifi.HardwareAddr.String())
	}
	return "00:00:00:00:00:00"
}

func formatMAC(b []byte) string {
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = fmt.Sprintf("%02X", x)
	}
	return strings.Join(parts, ":")
}
