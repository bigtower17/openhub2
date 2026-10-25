// ohweb is the OpenHub2 web panel for the Xiaomi Smart Home Hub 2: a
// single-page dashboard backed by a small JSON API. Authentication reuses
// the bcrypt hash of gatessh (/data/gatessh/root.hash).
package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

//go:embed web
var webFS embed.FS

var listenAddr = flag.String("addr", ":80", "listen address")

type server struct {
	hash []byte

	mu sync.Mutex
	ok [32]byte // SHA-256 of the last password bcrypt accepted
}

func main() {
	flag.Parse()
	raw, err := os.ReadFile("/data/gatessh/root.hash")
	if err != nil {
		log.Fatalf("read root.hash: %v", err)
	}
	s := &server{hash: bytes.TrimSpace(raw)}
	go sampler.run()

	web, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServerFS(web))
	mux.HandleFunc("/api/status", apiStatus)
	mux.HandleFunc("/api/history", apiHistory)
	mux.HandleFunc("/api/bluetooth", apiBluetooth)
	mux.HandleFunc("/api/logs", apiLogs)
	mux.HandleFunc("/api/wifi/scan", apiWifiScan)
	mux.HandleFunc("/api/wifi", post(apiWifi))
	mux.HandleFunc("/api/mode", post(apiMode))
	mux.HandleFunc("/api/system", post(apiSystem))
	mux.HandleFunc("/api/led", post(apiLED))
	mux.HandleFunc("/api/reboot", post(apiReboot))
	mux.HandleFunc("/api/zigbee/refresh", post(apiRescan))

	log.Printf("ohweb listening on %s", *listenAddr)
	log.Fatal(http.ListenAndServe(*listenAddr, s.auth(mux)))
}

func (s *server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, ok := r.BasicAuth()
		if !ok || !s.check(pass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="OpenHub2"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// check verifies the password. bcrypt takes a large fraction of a second on
// this CPU and the dashboard polls every few seconds, so the last accepted
// password is remembered as a SHA-256 digest.
func (s *server) check(pass string) bool {
	sum := sha256.Sum256([]byte(pass))
	s.mu.Lock()
	known := s.ok
	s.mu.Unlock()
	if known != ([32]byte{}) && subtle.ConstantTimeCompare(sum[:], known[:]) == 1 {
		return true
	}
	if bcrypt.CompareHashAndPassword(s.hash, []byte(pass)) != nil {
		return false
	}
	s.mu.Lock()
	s.ok = sum
	s.mu.Unlock()
	return true
}
