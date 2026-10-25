package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

const dataDir = "/data/openhub2"

// Status is everything the dashboard shows, served at /api/status.
type Status struct {
	Hostname  string    `json:"hostname"`
	System    System    `json:"system"`
	Resources Resources `json:"resources"`
	Network   Network   `json:"network"`
	Zigbee    Zigbee    `json:"zigbee"`
	Bluetooth Bluetooth `json:"bluetooth"`
	HA        HA        `json:"home_assistant"`
	LED       *LED      `json:"led,omitempty"`
	Time      string    `json:"time"`
}

type System struct {
	Userland    bool   `json:"userland"`
	Slot        string `json:"slot"`
	NextBoot    string `json:"next_boot"`
	CanUserland bool   `json:"can_userland"`
	Image       string `json:"image,omitempty"`
	Firmware    string `json:"firmware,omitempty"`
	Mode        string `json:"mode"`
	StockZigbee string `json:"stock_zigbee,omitempty"`
	UptimeSec   int    `json:"uptime_s"`
}

type Resources struct {
	CPU        float64 `json:"cpu"`
	CPUMHz     int     `json:"cpu_mhz"`
	MemUsedMB  int     `json:"mem_used_mb"`
	MemTotalMB int     `json:"mem_total_mb"`
	DataFreeKB int     `json:"data_free_kb"`
	DataSizeKB int     `json:"data_size_kb"`
}

type Network struct {
	IP       string `json:"ip"`
	MAC      string `json:"mac"`
	SSID     string `json:"ssid,omitempty"`
	RSSI     int    `json:"rssi,omitempty"`
	Ethernet bool   `json:"ethernet_link"`
	NTP      bool   `json:"ntp"`
}

type Zigbee struct {
	Bridge  string          `json:"bridge"`
	Network json.RawMessage `json:"network,omitempty"`
	Devices json.RawMessage `json:"devices,omitempty"`
	Updated string          `json:"updated,omitempty"`
}

type Bluetooth struct {
	Proxy string          `json:"proxy"`
	Info  json.RawMessage `json:"info,omitempty"`
}

type HA struct {
	ZHA       bool   `json:"zha"`
	ZHAClient string `json:"zha_client,omitempty"`
	ZHASince  string `json:"zha_since,omitempty"`
	ESPHome   int    `json:"esphome_clients"`
}

type LED struct {
	Color  string   `json:"color"`
	Colors []string `json:"colors"`
}

// LED colors understood by ohled (userland image).
var ledColors = []string{"green", "blue", "cyan", "purple", "orange", "white", "red", "off"}

func getStatus() Status {
	var st Status
	st.Hostname, _ = os.Hostname()
	st.Time = time.Now().UTC().Format(time.RFC3339)
	st.System = getSystem()
	st.Resources = getResources()
	st.Network = getNetwork()

	st.Zigbee.Bridge = running("zbbridge")
	if b, err := os.ReadFile("/tmp/zbbridge-network.json"); err == nil {
		var z struct {
			Network   json.RawMessage `json:"network"`
			Devices   json.RawMessage `json:"devices"`
			ScannedAt string          `json:"scanned_at"`
		}
		if json.Unmarshal(b, &z) == nil {
			st.Zigbee.Network, st.Zigbee.Devices, st.Zigbee.Updated = z.Network, z.Devices, z.ScannedAt
		}
	}
	if b, err := os.ReadFile("/tmp/zbbridge-client.json"); err == nil {
		var c struct{ Client, Since string }
		if json.Unmarshal(b, &c) == nil && c.Client != "" {
			st.HA.ZHA, st.HA.ZHAClient, st.HA.ZHASince = true, c.Client, c.Since
		}
	}

	st.Bluetooth.Proxy = running("btproxy")
	if st.Bluetooth.Proxy == "running" {
		if b, err := os.ReadFile("/tmp/btproxy.json"); err == nil && json.Valid(b) {
			st.Bluetooth.Info = b
			var c struct{ Clients int }
			json.Unmarshal(b, &c)
			st.HA.ESPHome = c.Clients
		}
	}

	if st.System.Userland {
		st.LED = &LED{Color: ledColor(), Colors: ledColors}
	}
	return st
}

func getSystem() System {
	s := System{Mode: "xiaomi", Slot: "B", NextBoot: "stock"}
	if b, err := os.ReadFile(dataDir + "/mode"); err == nil {
		s.Mode = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/proc/cmdline"); err == nil {
		s.Userland = strings.Contains(string(b), "oh=userland")
		if strings.Contains(string(b), "mtdblock7") {
			s.Slot = "A"
		}
	}
	if out, err := exec.Command(printenv(), "oh_next").Output(); err == nil && strings.Contains(string(out), "oh_try") {
		s.NextBoot = "OpenHub2"
	}
	_, err := os.Stat(dataDir + "/userland.flashed")
	s.CanUserland = err == nil || s.Userland
	if s.Userland {
		if v := osRelease("VERSION_ID"); v != "" {
			s.Image = v[:min(7, len(v))]
		}
	} else {
		if _, err := os.Stat("/tmp/openhub2-ready"); err == nil {
			s.StockZigbee = "disabled"
		} else {
			s.StockZigbee = "active"
		}
		if b, err := os.ReadFile("/data/fw_info"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if v, ok := strings.CutPrefix(line, "version="); ok {
					s.Firmware = strings.TrimSpace(v)
				}
			}
		}
	}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		var secs float64
		fmt.Sscanf(string(b), "%f", &secs)
		s.UptimeSec = int(secs)
	}
	return s
}

func getResources() Resources {
	r := Resources{CPU: sampler.lastCPU()}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		var total, avail int
		for _, line := range strings.Split(string(b), "\n") {
			fmt.Sscanf(line, "MemTotal: %d kB", &total)
			fmt.Sscanf(line, "MemAvailable: %d kB", &avail)
		}
		r.MemUsedMB, r.MemTotalMB = (total-avail)/1024, total/1024
	}
	if b, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_cur_freq"); err == nil {
		var khz int
		fmt.Sscanf(string(b), "%d", &khz)
		r.CPUMHz = khz / 1000
	}
	if out, err := exec.Command("df", "-k", "/data").Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if f := strings.Fields(lines[len(lines)-1]); len(f) >= 4 {
			fmt.Sscanf(f[1], "%d", &r.DataSizeKB)
			fmt.Sscanf(f[3], "%d", &r.DataFreeKB)
		}
	}
	return r
}

func getNetwork() Network {
	var n Network
	if out, err := exec.Command("ifconfig", "wlan0").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if i := strings.Index(line, "inet addr:"); i >= 0 {
				n.IP = strings.Fields(line[i+len("inet addr:"):])[0]
			}
		}
	}
	if b, err := os.ReadFile("/sys/class/net/wlan0/address"); err == nil {
		n.MAC = strings.ToUpper(strings.TrimSpace(string(b)))
	}
	if b, err := os.ReadFile("/sys/class/net/eth0/carrier"); err == nil {
		n.Ethernet = strings.TrimSpace(string(b)) == "1"
	}
	if out, err := exec.Command("wpa_cli", "-i", "wlan0", "status").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if v, ok := strings.CutPrefix(line, "ssid="); ok {
				n.SSID = v
			}
		}
	}
	if out, err := exec.Command("wpa_cli", "-i", "wlan0", "signal_poll").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if v, ok := strings.CutPrefix(line, "RSSI="); ok {
				fmt.Sscanf(v, "%d", &n.RSSI)
			}
		}
	}
	n.NTP = running("ntpd") == "running"
	return n
}

func ledColor() string {
	if b, err := os.ReadFile(dataDir + "/led"); err == nil {
		if c := strings.TrimSpace(string(b)); slices.Contains(ledColors, c) {
			return c
		}
	}
	return "green"
}

func running(name string) string {
	out, _ := exec.Command("pidof", name).Output()
	if len(bytes.TrimSpace(out)) > 0 {
		return "running"
	}
	return "stopped"
}

// printenv returns the U-Boot environment tool of the running system.
func printenv() string {
	if _, err := os.Stat("/etc/fw_printenv"); err == nil {
		return "/etc/fw_printenv"
	}
	return "fw_printenv"
}

func osRelease(key string) string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}
