package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf(format, args...)})
}

func post(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		h(w, r)
	}
}

func rebootSoon() {
	go func() {
		time.Sleep(time.Second)
		exec.Command("sync").Run()
		exec.Command("reboot").Run()
	}()
}

func apiStatus(w http.ResponseWriter, r *http.Request) { reply(w, getStatus()) }

func apiHistory(w http.ResponseWriter, r *http.Request) {
	d := map[string]time.Duration{"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour}[r.FormValue("range")]
	if d == 0 {
		d = time.Hour
	}
	reply(w, map[string]any{"interval_s": int(sampleEvery.Seconds()), "points": sampler.history(d, 240)})
}

func apiBluetooth(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile("/tmp/btproxy-devices.json")
	if err != nil || !json.Valid(b) {
		b = []byte("[]")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

var logFiles = map[string]string{
	"zbbridge": "/tmp/zbbridge.log",
	"btproxy":  "/tmp/btproxy.log",
	"gatessh":  "/tmp/gatessh.log",
	"ohweb":    "/tmp/ohweb.log",
	"ohbutton": "/tmp/ohbutton.log",
	"prepare":  "/tmp/openhub2-prepare.log",
	"userland": dataDir + "/userland.log",
	"wifi":     dataDir + "/wifi-fallback.log",
	"dmesg":    "",
}

func apiLogs(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	path, ok := logFiles[name]
	if !ok {
		names := make([]string, 0, len(logFiles))
		for n := range logFiles {
			names = append(names, n)
		}
		sort.Strings(names)
		reply(w, map[string]any{"logs": names})
		return
	}
	var out []byte
	if name == "dmesg" {
		out, _ = exec.Command("sh", "-c", "dmesg | tail -n 200").Output()
	} else {
		out, _ = exec.Command("tail", "-n", "200", path).Output()
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if len(out) == 0 {
		out = []byte("(empty)\n")
	}
	w.Write(out)
}

type wifiNet struct {
	SSID   string `json:"ssid"`
	Signal int    `json:"signal"`
	Secure bool   `json:"secure"`
	MHz    int    `json:"mhz"`
}

func apiWifiScan(w http.ResponseWriter, r *http.Request) {
	exec.Command("wpa_cli", "-i", "wlan0", "scan").Run()
	time.Sleep(4 * time.Second)
	out, err := exec.Command("wpa_cli", "-i", "wlan0", "scan_results").Output()
	if err != nil {
		fail(w, http.StatusInternalServerError, "scan: %v", err)
		return
	}
	best := map[string]wifiNet{}
	for _, line := range strings.Split(string(out), "\n")[1:] {
		f := strings.SplitN(line, "\t", 5) // bssid, frequency, signal, flags, ssid
		if len(f) < 5 || f[4] == "" {
			continue
		}
		mhz, _ := strconv.Atoi(f[1])
		sig, _ := strconv.Atoi(f[2])
		n := wifiNet{SSID: f[4], Signal: sig, Secure: strings.Contains(f[3], "WPA") || strings.Contains(f[3], "WEP"), MHz: mhz}
		if old, ok := best[n.SSID]; !ok || n.Signal > old.Signal {
			best[n.SSID] = n
		}
	}
	list := make([]wifiNet, 0, len(best))
	for _, n := range best {
		list = append(list, n)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Signal > list[j].Signal })
	reply(w, list)
}

func apiWifi(w http.ResponseWriter, r *http.Request) {
	ssid := strings.TrimSpace(r.FormValue("ssid"))
	pass := r.FormValue("password")
	if ssid == "" {
		fail(w, http.StatusBadRequest, "SSID is required")
		return
	}
	if strings.ContainsAny(ssid+pass, "\"\n\r") {
		fail(w, http.StatusBadRequest, "quotes and line breaks are not supported in SSID or password")
		return
	}
	if pass != "" && (len(pass) < 8 || len(pass) > 63) {
		fail(w, http.StatusBadRequest, "a WPA password has 8 to 63 characters")
		return
	}
	if getSystem().Userland {
		conf := "ctrl_interface=/var/run/wpa_supplicant\nnetwork={\n\tssid=\"" + ssid + "\"\n\tscan_ssid=1\n"
		if pass != "" {
			conf += "\tpsk=\"" + pass + "\"\n\tkey_mgmt=WPA-PSK\n}\n"
		} else {
			conf += "\tkey_mgmt=NONE\n}\n"
		}
		if err := os.WriteFile(dataDir+"/wpa_supplicant.conf", []byte(conf), 0o600); err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		exec.Command("sync").Run()
		if out, err := exec.Command("wpa_cli", "-i", "wlan0", "reconfigure").CombinedOutput(); err != nil {
			fail(w, http.StatusInternalServerError, "wpa_cli: %v %s", err, out)
			return
		}
	} else if out, err := exec.Command("/bin/reset_wifi_info.sh", ssid, pass).CombinedOutput(); err != nil {
		fail(w, http.StatusInternalServerError, "%v: %s", err, strings.TrimSpace(string(out)))
		return
	}
	reply(w, map[string]string{"message": fmt.Sprintf("Wi-Fi set to %q, reconnecting", ssid)})
}

func apiMode(w http.ResponseWriter, r *http.Request) {
	m := r.FormValue("mode")
	if m != "openhub2" && m != "xiaomi" {
		fail(w, http.StatusBadRequest, "invalid mode")
		return
	}
	if err := os.WriteFile(dataDir+"/mode", []byte(m+"\n"), 0o644); err != nil {
		fail(w, http.StatusInternalServerError, "%v", err)
		return
	}
	rebootSoon()
	reply(w, map[string]string{"message": "mode " + m + ", rebooting"})
}

func apiSystem(w http.ResponseWriter, r *http.Request) {
	action := map[string]string{"stock": "disable", "openhub2": "enable"}[r.FormValue("to")]
	if action == "" {
		fail(w, http.StatusBadRequest, "invalid target")
		return
	}
	if out, err := exec.Command("sh", dataDir+"/userland.sh", action).CombinedOutput(); err != nil {
		fail(w, http.StatusInternalServerError, "userland.sh %s: %v %s", action, err, strings.TrimSpace(string(out)))
		return
	}
	rebootSoon()
	reply(w, map[string]string{"message": "rebooting"})
}

func apiLED(w http.ResponseWriter, r *http.Request) {
	c := r.FormValue("color")
	if !slices.Contains(ledColors, c) {
		fail(w, http.StatusBadRequest, "invalid color")
		return
	}
	if err := os.WriteFile(dataDir+"/led", []byte(c+"\n"), 0o644); err != nil {
		fail(w, http.StatusInternalServerError, "%v", err)
		return
	}
	// Run without capturing output: ohled may leave a blink loop behind.
	if err := exec.Command("ohled", c).Run(); err != nil {
		fail(w, http.StatusInternalServerError, "ohled: %v", err)
		return
	}
	reply(w, map[string]string{"color": c})
}

func apiReboot(w http.ResponseWriter, r *http.Request) {
	rebootSoon()
	reply(w, map[string]string{"message": "rebooting"})
}

func apiRescan(w http.ResponseWriter, r *http.Request) {
	exec.Command("killall", "-USR1", "zbbridge").Run()
	reply(w, map[string]string{"message": "refresh requested"})
}
