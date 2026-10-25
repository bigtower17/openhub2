// ohweb is a lightweight web panel for the Xiaomi Smart Home Hub 2 running
// OpenHub2. It shows system status, allows mode switching, Wi-Fi
// configuration and log viewing. Authentication reuses the bcrypt hash
// from gatessh (/data/gatessh/root.hash).
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const dataDir = "/data/openhub2"

var listenAddr = flag.String("addr", ":80", "listen address")

type server struct {
	hash []byte
	tmpl *template.Template
}

func main() {
	flag.Parse()
	raw, err := os.ReadFile("/data/gatessh/root.hash")
	if err != nil {
		log.Fatalf("read root.hash: %v", err)
	}
	s := &server{hash: bytes.TrimSpace(raw)}
	s.tmpl = template.Must(template.New("").Funcs(template.FuncMap{
		"join": strings.Join,
		"ge":   func(a, b uint8) bool { return a >= b },
	}).Parse(templates))

	http.HandleFunc("/", s.auth(s.dashboard))
	http.HandleFunc("/wifi", s.auth(s.wifi))
	http.HandleFunc("/logs", s.auth(s.logs))
	http.HandleFunc("/mode", s.auth(s.mode))
	http.HandleFunc("/reboot", s.auth(s.reboot))
	http.HandleFunc("/rescan", s.auth(s.rescan))
	http.HandleFunc("/system", s.auth(s.system))
	http.HandleFunc("/bluetooth", s.auth(s.bluetooth))

	log.Printf("ohweb listening on %s", *listenAddr)
	log.Fatal(http.ListenAndServe(*listenAddr, nil))
}

func (s *server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, pass, ok := r.BasicAuth()
		if !ok || bcrypt.CompareHashAndPassword(s.hash, []byte(pass)) != nil {
			w.Header().Set("WWW-Authenticate", `Basic realm="OpenHub2"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// --- status -----------------------------------------------------------------

type statusData struct {
	Mode        string
	StockZigbee string
	ZbBridge    string
	Uptime      string
	IPs         []string
	FreeData    string
	NTP         bool
	FwVer       string
	MAC         string
	Zigbee      *zigbeeInfo
	BTProxy     string
	BT          *btInfo

	Userland    bool   // running the OpenHub2 userland (slot A)
	NextBoot    string // "OpenHub2" or "stock"
	CanUserland bool   // slot A holds a flashed userland image
	Image       string
	CPU         string
	Memory      string
	CPUFreq     string
}

func getStatus() statusData {
	st := statusData{Mode: "xiaomi"}

	if b, err := os.ReadFile(dataDir + "/mode"); err == nil {
		st.Mode = strings.TrimSpace(string(b))
	}
	if _, err := os.Stat("/tmp/openhub2-ready"); err == nil {
		st.StockZigbee = "disabled"
	} else {
		st.StockZigbee = "active"
	}
	if alive("zbbridge") {
		st.ZbBridge = "running"
	} else {
		st.ZbBridge = "stopped"
	}
	st.NTP = alive("ntpd")

	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		var secs float64
		fmt.Sscanf(string(b), "%f", &secs)
		d := time.Duration(secs) * time.Second
		h := int(d.Hours())
		st.Uptime = fmt.Sprintf("%dd %dh %dm", h/24, h%24, int(d.Minutes())%60)
	}
	if out, err := exec.Command("ifconfig").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "inet addr:") {
				ip := strings.Fields(strings.TrimPrefix(line, "inet addr:"))[0]
				if ip != "127.0.0.1" {
					st.IPs = append(st.IPs, ip)
				}
			}
		}
	}
	if out, err := exec.Command("df", "-k", "/data").Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) >= 2 {
			ff := strings.Fields(lines[len(lines)-1])
			if len(ff) >= 4 {
				st.FreeData = ff[3] + " KB"
			}
		}
	}
	if b, err := os.ReadFile("/data/fw_info"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "version=") {
				st.FwVer = strings.TrimPrefix(line, "version=")
			}
		}
	}
	if b, err := os.ReadFile("/sys/class/net/wlan0/address"); err == nil {
		st.MAC = strings.ToUpper(strings.TrimSpace(string(b)))
	}
	st.Zigbee = getZigbeeInfo()
	st.BTProxy = "stopped"
	if alive("btproxy") {
		st.BTProxy = "running"
		st.BT = getBTInfo()
	}
	getSystem(&st)
	return st
}

// --- userland / machine -----------------------------------------------------

func getSystem(st *statusData) {
	if b, err := os.ReadFile("/proc/cmdline"); err == nil {
		st.Userland = strings.Contains(string(b), "oh=userland")
	}
	st.NextBoot = "stock"
	printenv := "fw_printenv"
	if _, err := os.Stat("/etc/fw_printenv"); err == nil {
		printenv = "/etc/fw_printenv"
	}
	if out, err := exec.Command(printenv, "oh_next").Output(); err == nil && strings.Contains(string(out), "oh_try") {
		st.NextBoot = "OpenHub2"
	}
	_, err := os.Stat(dataDir + "/userland.flashed")
	st.CanUserland = err == nil || st.Userland
	if st.Userland {
		if b, err := os.ReadFile("/etc/os-release"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if v, ok := strings.CutPrefix(line, "VERSION_ID="); ok {
					st.Image = v[:min(7, len(v))]
				}
			}
		}
	}
	st.CPU = cpuUsage()
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		var total, avail int
		for _, line := range strings.Split(string(b), "\n") {
			fmt.Sscanf(line, "MemTotal: %d kB", &total)
			fmt.Sscanf(line, "MemAvailable: %d kB", &avail)
		}
		if total > 0 {
			st.Memory = fmt.Sprintf("%d / %d MB", (total-avail)/1024, total/1024)
		}
	}
	if b, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_cur_freq"); err == nil {
		var khz int
		fmt.Sscanf(string(b), "%d", &khz)
		st.CPUFreq = fmt.Sprintf("%d MHz", khz/1000)
	}
}

// cpuUsage samples /proc/stat over 200 ms.
func cpuUsage() string {
	read := func() (busy, total uint64) {
		b, err := os.ReadFile("/proc/stat")
		if err != nil {
			return 0, 0
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
	b1, t1 := read()
	time.Sleep(200 * time.Millisecond)
	b2, t2 := read()
	if t2 <= t1 {
		return ""
	}
	return fmt.Sprintf("%d%%", (b2-b1)*100/(t2-t1))
}

func alive(name string) bool {
	out, _ := exec.Command("pidof", name).Output()
	return len(bytes.TrimSpace(out)) > 0
}

// --- zigbee network info (from zbbridge scan) --------------------------------

type zigbeeNetwork struct {
	Status       string `json:"status"`
	Channel      uint8  `json:"channel"`
	PanID        uint16 `json:"pan_id"`
	ExtPanID     string `json:"ext_pan_id"`
	EUI64        string `json:"eui64"`
	EZSPVersion  uint8  `json:"ezsp_version"`
	StackVersion string `json:"stack_version"`
}

type zigbeeDevice struct {
	EUI64    string `json:"eui64"`
	ShortID  uint16 `json:"short_id"`
	LQI      uint8  `json:"lqi"`
	RSSI     int8   `json:"rssi"`
	LastSeen string `json:"last_seen"`
}

func (d zigbeeDevice) Seen() string {
	t, err := time.Parse(time.RFC3339, d.LastSeen)
	if err != nil {
		return "—"
	}
	s := time.Since(t).Round(time.Second)
	switch {
	case s < time.Minute:
		return fmt.Sprintf("%ds ago", int(s.Seconds()))
	case s < time.Hour:
		return fmt.Sprintf("%dm ago", int(s.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(s.Hours()))
}

type zigbeeInfo struct {
	Network   zigbeeNetwork  `json:"network"`
	Devices   []zigbeeDevice `json:"devices"`
	ScannedAt string         `json:"scanned_at"`
}

func getZigbeeInfo() *zigbeeInfo {
	b, err := os.ReadFile("/tmp/zbbridge-network.json")
	if err != nil {
		return nil
	}
	var info zigbeeInfo
	if json.Unmarshal(b, &info) != nil {
		return nil
	}
	return &info
}

// --- bluetooth proxy status (from btproxy) ----------------------------------

type btInfo struct {
	Name           string `json:"name"`
	BluetoothMAC   string `json:"bluetooth_mac"`
	Controller     string `json:"controller"`
	Clients        int    `json:"clients"`
	Advertisements int    `json:"advertisements"`
	Devices        int    `json:"devices_5min"`
	UpdatedAt      string `json:"updated_at"`
}

func getBTInfo() *btInfo {
	b, err := os.ReadFile("/tmp/btproxy.json")
	if err != nil {
		return nil
	}
	var info btInfo
	if json.Unmarshal(b, &info) != nil {
		return nil
	}
	return &info
}

type bleDevice struct {
	Address      string `json:"address"`
	AddressType  uint8  `json:"address_type"`
	Name         string `json:"name"`
	RSSI         int8   `json:"rssi"`
	Manufacturer string `json:"manufacturer"`
	Services     string `json:"services"`
	LastSeen     string `json:"last_seen"`
}

func (d bleDevice) Seen() string { return zigbeeDevice{LastSeen: d.LastSeen}.Seen() }

// --- handlers ---------------------------------------------------------------

func (s *server) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.tmpl.ExecuteTemplate(w, "dashboard", getStatus())
}

func (s *server) wifi(w http.ResponseWriter, r *http.Request) {
	data := map[string]string{}
	if r.Method == "POST" {
		ssid := strings.TrimSpace(r.FormValue("ssid"))
		pass := r.FormValue("password")
		if ssid == "" {
			data["Error"] = "SSID is required."
		} else {
			out, err := exec.Command("/bin/reset_wifi_info.sh", ssid, pass).CombinedOutput()
			if err != nil {
				data["Error"] = fmt.Sprintf("%v — %s", err, bytes.TrimSpace(out))
			} else {
				data["Success"] = fmt.Sprintf("Wi-Fi set to %q. The hub is reconnecting.", ssid)
			}
		}
	}
	s.tmpl.ExecuteTemplate(w, "wifi", data)
}

type logFile struct {
	Name, Content string
}

func (s *server) logs(w http.ResponseWriter, r *http.Request) {
	entries := []struct{ name, path string }{
		{"gatessh", "/tmp/gatessh.log"},
		{"zbbridge", "/tmp/zbbridge.log"},
		{"btproxy", "/tmp/btproxy.log"},
		{"prepare", "/tmp/openhub2-prepare.log"},
		{"wifi-fallback", dataDir + "/wifi-fallback.log"},
		{"dmesg", ""},
	}
	var out []logFile
	for _, e := range entries {
		content := "(no log)"
		if e.path == "" {
			if b, err := exec.Command("dmesg").Output(); err == nil {
				lines := strings.Split(string(b), "\n")
				if len(lines) > 80 {
					lines = lines[len(lines)-80:]
				}
				content = strings.Join(lines, "\n")
			}
		} else if b, err := exec.Command("tail", "-n", "80", e.path).Output(); err == nil && len(b) > 0 {
			content = string(b)
		}
		out = append(out, logFile{e.name, content})
	}
	s.tmpl.ExecuteTemplate(w, "logs", out)
}

func (s *server) mode(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	m := r.FormValue("mode")
	if m != "openhub2" && m != "xiaomi" {
		http.Error(w, "invalid mode", http.StatusBadRequest)
		return
	}
	if err := os.WriteFile(dataDir+"/mode", []byte(m+"\n"), 0644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = exec.Command("sync").Run()
	go func() {
		time.Sleep(time.Second)
		exec.Command("reboot").Run()
	}()
	s.tmpl.ExecuteTemplate(w, "rebooting", m)
}

func (s *server) rescan(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	exec.Command("killall", "-USR1", "zbbridge").Run()
	time.Sleep(time.Second)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// system switches between the OpenHub2 userland and the stock firmware.
func (s *server) system(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	var action, label string
	switch r.FormValue("to") {
	case "stock":
		action, label = "disable", "stock"
	case "openhub2":
		action, label = "enable", "OpenHub2"
	default:
		http.Error(w, "invalid target", http.StatusBadRequest)
		return
	}
	out, err := exec.Command("sh", dataDir+"/userland.sh", action).CombinedOutput()
	if err != nil {
		http.Error(w, fmt.Sprintf("userland.sh %s: %v\n%s", action, err, out), http.StatusInternalServerError)
		return
	}
	go func() {
		time.Sleep(time.Second)
		exec.Command("reboot").Run()
	}()
	s.tmpl.ExecuteTemplate(w, "rebooting", label)
}

func (s *server) bluetooth(w http.ResponseWriter, r *http.Request) {
	data := struct {
		Running bool
		Devices []bleDevice
	}{Running: alive("btproxy")}
	if b, err := os.ReadFile("/tmp/btproxy-devices.json"); err == nil {
		json.Unmarshal(b, &data.Devices)
	}
	// named devices first, then by signal strength
	sort.SliceStable(data.Devices, func(i, j int) bool {
		a, b := data.Devices[i], data.Devices[j]
		if (a.Name != "") != (b.Name != "") {
			return a.Name != ""
		}
		return a.RSSI > b.RSSI
	})
	s.tmpl.ExecuteTemplate(w, "bluetooth", data)
}

func (s *server) reboot(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	go func() {
		time.Sleep(time.Second)
		exec.Command("reboot").Run()
	}()
	s.tmpl.ExecuteTemplate(w, "rebooting", "")
}

// --- templates --------------------------------------------------------------

const templates = `
{{define "head"}}<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>OpenHub2</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:system-ui,-apple-system,sans-serif;background:#f4f4f5;color:#27272a;line-height:1.5}
nav{background:#18181b;padding:10px 16px;display:flex;align-items:center;gap:16px;flex-wrap:wrap}
nav .brand{color:#fff;font-weight:700;font-size:1.1em;margin-right:auto}
nav a{color:#a1a1aa;text-decoration:none;font-size:.9em}
nav a:hover,nav a.on{color:#fff}
.w{max-width:600px;margin:0 auto;padding:16px}
.card{background:#fff;border-radius:8px;padding:16px;margin:12px 0;box-shadow:0 1px 2px rgba(0,0,0,.06)}
.card h2{font-size:.75em;color:#71717a;margin-bottom:8px;text-transform:uppercase;letter-spacing:.06em;font-weight:600}
.row{display:flex;justify-content:space-between;padding:6px 0;border-bottom:1px solid #f4f4f5}
.row:last-child{border:none}
.lbl{color:#71717a;font-size:.93em}
.val{font-weight:500}
.ok{color:#16a34a}.off{color:#a1a1aa}.warn{color:#ea580c}
button{background:#18181b;color:#fff;border:none;padding:8px 20px;border-radius:6px;cursor:pointer;font-size:.9em}
button:hover{background:#27272a}
.danger{background:#dc2626}
.danger:hover{background:#b91c1c}
input[type=text],input[type=password]{width:100%;padding:8px 12px;border:1px solid #d4d4d8;border-radius:6px;font-size:1em;margin:4px 0 12px}
.act{display:flex;gap:8px;margin-top:8px;flex-wrap:wrap}
.msg{padding:10px 14px;border-radius:6px;margin-bottom:12px;font-size:.93em}
.msg.err{background:#fef2f2;color:#dc2626;border:1px solid #fecaca}
.msg.ok{background:#f0fdf4;color:#16a34a;border:1px solid #bbf7d0}
pre{background:#18181b;color:#e4e4e7;padding:12px;border-radius:6px;overflow-x:auto;font-size:.8em;
    white-space:pre-wrap;word-break:break-all;max-height:350px;overflow-y:auto;line-height:1.4}
label{font-size:.9em;color:#52525b;font-weight:500}
.center{text-align:center;padding:40px 0;color:#71717a}
</style>
</head>
<body>
<nav>
<span class="brand">OpenHub2</span>
<a href="/">Status</a>
<a href="/bluetooth">Bluetooth</a>
<a href="/wifi">Wi-Fi</a>
<a href="/logs">Logs</a>
</nav>
<div class="w">
{{end}}

{{define "foot"}}
</div></body></html>
{{end}}

{{define "dashboard"}}{{template "head"}}
<div class="card">
<h2>System</h2>
<div class="row"><span class="lbl">Running</span>
  <span class="val {{if .Userland}}ok{{else}}warn{{end}}">{{if .Userland}}OpenHub2 userland (slot A){{else}}Xiaomi stock (slot B){{end}}</span></div>
{{if .CanUserland}}<div class="row"><span class="lbl">Next boot</span><span class="val">{{.NextBoot}}</span></div>{{end}}
{{if .Image}}<div class="row"><span class="lbl">Image</span><span class="val">{{.Image}}</span></div>{{end}}
{{if not .Userland}}
<div class="row"><span class="lbl">Mode</span>
  <span class="val {{if eq .Mode "openhub2"}}ok{{else}}warn{{end}}">{{.Mode}}</span></div>
<div class="row"><span class="lbl">Stock Zigbee</span>
  <span class="val {{if eq .StockZigbee "disabled"}}off{{else}}ok{{end}}">{{.StockZigbee}}</span></div>
{{end}}
{{if .CPU}}<div class="row"><span class="lbl">CPU</span><span class="val">{{.CPU}}{{if .CPUFreq}} · {{.CPUFreq}}{{end}}</span></div>{{end}}
{{if .Memory}}<div class="row"><span class="lbl">Memory</span><span class="val">{{.Memory}}</span></div>{{end}}
<div class="row"><span class="lbl">zbbridge</span>
  <span class="val {{if eq .ZbBridge "running"}}ok{{else}}off{{end}}">{{.ZbBridge}}{{if eq .ZbBridge "running"}} · tcp :8888{{end}}</span></div>
<div class="row"><span class="lbl">NTP</span>
  <span class="val {{if .NTP}}ok{{else}}off{{end}}">{{if .NTP}}running{{else}}stopped{{end}}</span></div>
<div class="row"><span class="lbl">Uptime</span><span class="val">{{.Uptime}}</span></div>
<div class="row"><span class="lbl">IP</span><span class="val">{{join .IPs ", "}}</span></div>
{{if .MAC}}<div class="row"><span class="lbl">MAC</span><span class="val">{{.MAC}}</span></div>{{end}}
<div class="row"><span class="lbl">/data free</span><span class="val">{{.FreeData}}</span></div>
{{if .FwVer}}<div class="row"><span class="lbl">Firmware</span><span class="val">{{.FwVer}}</span></div>{{end}}
</div>

{{if not .Zigbee}}
<div class="card">
<h2>Zigbee Network</h2>
<p style="color:#a1a1aa;font-size:.9em;padding:8px 0">No scan data available.</p>
<form method="POST" action="/rescan"><button style="font-size:.82em;padding:6px 14px">Scan NCP</button></form>
</div>
{{else}}
<div class="card">
<h2>Zigbee Network</h2>
<div class="row"><span class="lbl">Status</span>
  <span class="val {{if eq .Zigbee.Network.Status "joined"}}ok{{else}}warn{{end}}">{{.Zigbee.Network.Status}}</span></div>
<div class="row"><span class="lbl">Channel</span><span class="val">{{.Zigbee.Network.Channel}}</span></div>
<div class="row"><span class="lbl">PAN ID</span><span class="val">0x{{printf "%04X" .Zigbee.Network.PanID}}</span></div>
{{if .Zigbee.Network.ExtPanID}}<div class="row"><span class="lbl">Ext PAN ID</span><span class="val" style="font-size:.85em">{{.Zigbee.Network.ExtPanID}}</span></div>{{end}}
{{if .Zigbee.Network.EUI64}}<div class="row"><span class="lbl">Coordinator</span><span class="val" style="font-size:.85em">{{.Zigbee.Network.EUI64}}</span></div>{{end}}
<div class="row"><span class="lbl">EZSP / Stack</span><span class="val">v{{.Zigbee.Network.EZSPVersion}} / {{.Zigbee.Network.StackVersion}}</span></div>
{{if .Zigbee.ScannedAt}}<div class="row"><span class="lbl">Updated</span><span class="val" style="font-size:.85em">{{.Zigbee.ScannedAt}}</span></div>{{end}}
<div style="margin-top:8px">
<form method="POST" action="/rescan"><button style="font-size:.82em;padding:6px 14px">Refresh</button></form>
</div>
</div>

{{if .Zigbee.Devices}}
<div class="card">
<h2>Zigbee Devices ({{len .Zigbee.Devices}})</h2>
<table style="width:100%;font-size:.88em;border-collapse:collapse">
<tr style="text-align:left;border-bottom:2px solid #e4e4e7">
  <th style="padding:6px 4px;color:#71717a;font-weight:600;font-size:.82em">IEEE ADDRESS</th>
  <th style="padding:6px 4px;color:#71717a;font-weight:600;font-size:.82em">NWK</th>
  <th style="padding:6px 4px;color:#71717a;font-weight:600;font-size:.82em">LQI</th>
  <th style="padding:6px 4px;color:#71717a;font-weight:600;font-size:.82em">RSSI</th>
  <th style="padding:6px 4px;color:#71717a;font-weight:600;font-size:.82em">SEEN</th>
</tr>
{{range .Zigbee.Devices}}
<tr style="border-bottom:1px solid #f4f4f5">
  <td style="padding:6px 4px;font-family:monospace;font-size:.9em">{{if .EUI64}}{{.EUI64}}{{else}}<span class="off">unknown</span>{{end}}</td>
  <td style="padding:6px 4px">0x{{printf "%04X" .ShortID}}</td>
  <td style="padding:6px 4px">
    <span class="{{if ge .LQI 170}}ok{{else if ge .LQI 85}}warn{{else}}off{{end}}">{{.LQI}}</span>
  </td>
  <td style="padding:6px 4px">{{if .RSSI}}{{.RSSI}} dBm{{else}}—{{end}}</td>
  <td style="padding:6px 4px;font-size:.9em">{{.Seen}}</td>
</tr>
{{end}}
</table>
</div>
{{end}}
{{end}}

<div class="card">
<h2>Bluetooth Proxy</h2>
<div class="row"><span class="lbl">btproxy</span>
  <span class="val {{if eq .BTProxy "running"}}ok{{else}}off{{end}}">{{.BTProxy}}{{if eq .BTProxy "running"}} · tcp :6053{{end}}</span></div>
{{with .BT}}
<div class="row"><span class="lbl">Name</span><span class="val" style="font-size:.85em">{{.Name}}</span></div>
<div class="row"><span class="lbl">Bluetooth MAC</span><span class="val" style="font-size:.85em">{{.BluetoothMAC}}</span></div>
<div class="row"><span class="lbl">Home Assistant</span>
  <span class="val {{if .Clients}}ok{{else}}warn{{end}}">{{if .Clients}}connected ({{.Clients}}){{else}}not connected{{end}}</span></div>
<div class="row"><span class="lbl">Devices (last 5 min)</span><span class="val">{{.Devices}}</span></div>
<div class="row"><span class="lbl">Advertisements</span><span class="val">{{.Advertisements}}</span></div>
<div class="row"><span class="lbl">Controller</span><span class="val" style="font-size:.85em">{{.Controller}}</span></div>
{{end}}
</div>

<div class="card">
<h2>Actions</h2>
<div class="act">
{{if .Userland}}
<form method="POST" action="/system" onsubmit="return confirm('Boot the stock Xiaomi system? The hub will reboot. Three button presses do the same.')">
<input type="hidden" name="to" value="stock">
<button>Switch to stock</button>
</form>
{{else if .CanUserland}}
<form method="POST" action="/system" onsubmit="return confirm('Boot the OpenHub2 userland? The hub will reboot; a failed boot falls back to stock.')">
<input type="hidden" name="to" value="openhub2">
<button>Boot OpenHub2</button>
</form>
{{end}}
{{if .Userland}}{{else if eq .Mode "xiaomi"}}
<form method="POST" action="/mode" onsubmit="return confirm('Switch to OpenHub2? The hub will reboot.')">
<input type="hidden" name="mode" value="openhub2">
<button>Switch to OpenHub2</button>
</form>
{{else}}
<form method="POST" action="/mode" onsubmit="return confirm('Switch to Xiaomi mode? The hub will reboot.')">
<input type="hidden" name="mode" value="xiaomi">
<button>Switch to Xiaomi</button>
</form>
{{end}}
<form method="POST" action="/reboot" onsubmit="return confirm('Reboot the hub?')">
<button class="danger">Reboot</button>
</form>
</div>
</div>
{{template "foot"}}{{end}}

{{define "bluetooth"}}{{template "head"}}
<div class="card">
<h2>Bluetooth devices ({{len .Devices}}, last 10 min)</h2>
{{if not .Running}}<p class="off" style="padding:8px 0">btproxy is not running.</p>{{end}}
{{if .Devices}}
<table style="width:100%;font-size:.85em;border-collapse:collapse">
<tr style="text-align:left;border-bottom:2px solid #e4e4e7;color:#71717a;font-size:.82em">
  <th style="padding:6px 4px">ADDRESS</th><th style="padding:6px 4px">NAME / TYPE</th>
  <th style="padding:6px 4px">RSSI</th><th style="padding:6px 4px">SEEN</th>
</tr>
{{range .Devices}}
<tr style="border-bottom:1px solid #f4f4f5">
  <td style="padding:6px 4px;font-family:monospace;font-size:.9em">{{.Address}}{{if eq .AddressType 1}} <span class="off">rnd</span>{{end}}</td>
  <td style="padding:6px 4px">{{if .Name}}<b>{{.Name}}</b>{{end}}
    {{if .Services}}<span class="ok">{{.Services}}</span>{{end}}
    {{if .Manufacturer}}<span class="off">{{.Manufacturer}}</span>{{end}}</td>
  <td style="padding:6px 4px"><span class="{{if gt .RSSI -70}}ok{{else if gt .RSSI -85}}warn{{else}}off{{end}}">{{.RSSI}}</span></td>
  <td style="padding:6px 4px;font-size:.9em">{{.Seen}}</td>
</tr>
{{end}}
</table>
{{end}}
<p style="margin-top:10px;font-size:.8em;color:#a1a1aa">Refreshes every 15 s. Home Assistant receives all of these through the ESPHome Bluetooth proxy.</p>
</div>
<script>setTimeout(function(){location.reload()},15000)</script>
{{template "foot"}}{{end}}

{{define "wifi"}}{{template "head"}}
<div class="card">
<h2>Wi-Fi Configuration</h2>
{{if .Error}}<div class="msg err">{{.Error}}</div>{{end}}
{{if .Success}}<div class="msg ok">{{.Success}}</div>{{end}}
<form method="POST" action="/wifi">
<label for="ssid">SSID</label>
<input type="text" id="ssid" name="ssid" required autocomplete="off">
<label for="password">Password</label>
<input type="password" id="password" name="password" autocomplete="off">
<button>Apply</button>
</form>
<p style="margin-top:12px;font-size:.82em;color:#a1a1aa">
Changes Wi-Fi credentials and reconnects. Leave password empty for an open network.
</p>
</div>
{{template "foot"}}{{end}}

{{define "logs"}}{{template "head"}}
{{range .}}
<div class="card">
<h2>{{.Name}}</h2>
<pre>{{.Content}}</pre>
</div>
{{end}}
{{template "foot"}}{{end}}

{{define "rebooting"}}<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta http-equiv="refresh" content="30;url=/">
<title>OpenHub2 — rebooting</title>
<style>
body{font-family:system-ui,sans-serif;display:flex;justify-content:center;align-items:center;
     min-height:100vh;background:#f4f4f5;color:#27272a;text-align:center}
.box{padding:32px}
h1{font-size:1.2em;margin-bottom:8px}
p{color:#71717a;font-size:.95em}
</style>
</head>
<body>
<div class="box">
<h1>Rebooting{{if .}} → {{.}}{{end}}</h1>
<p>The hub is restarting. This page will reload in 30 seconds.</p>
</div>
</body>
</html>{{end}}
`
