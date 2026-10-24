package miio

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

// RecoverKey asks the hub to upload /data/miio/device.conf via the
// async_device_feedback_log diagnostic method, decrypts it and returns
// the 16-char gateway key. Only the device token is needed (no cloud).
//
// The caller must be on the same LAN as the hub. A temporary HTTP server
// is started to receive the upload. callbackIP may be empty; it is then
// auto-detected by routing to the hub.
func (d *Device) RecoverKey(ctx context.Context, callbackIP string) (string, error) {
	if d.deviceID == 0 {
		if err := d.Hello(); err != nil {
			return "", err
		}
	}

	aesKey := make([]byte, 16)
	aesIV := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, aesKey); err != nil {
		return "", err
	}
	if _, err := io.ReadFull(rand.Reader, aesIV); err != nil {
		return "", err
	}

	if callbackIP == "" {
		ip, err := localIPFor(d.addr)
		if err != nil {
			return "", fmt.Errorf("detect local IP: %w", err)
		}
		callbackIP = ip
	}

	recv := newReceiver(d.addr, aesKey, aesIV)
	if err := recv.Start(callbackIP); err != nil {
		return "", err
	}
	defer recv.Close()

	params := map[string]any{
		"encryptKey": base64.URLEncoding.EncodeToString(aesKey),
		"iv":         base64.URLEncoding.EncodeToString(aesIV),
		"url":        recv.URL(),
		"fileList":   []string{"/data/miio/device.conf"},
		"filelist":   []string{"/data/miio/device.conf"},
	}
	// The hub may not acknowledge but still upload, so ignore call errors.
	d.Call("async_device_feedback_log", params)

	select {
	case r := <-recv.result:
		return r.key, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// --- HTTP receiver ----------------------------------------------------------

type recvResult struct {
	key string
	err error
}

type receiver struct {
	gatewayHost string
	aesKey      []byte
	aesIV       []byte
	path        string
	server      *http.Server
	listener    net.Listener
	result      chan recvResult
	once        sync.Once
}

func newReceiver(gatewayAddr string, key, iv []byte) *receiver {
	host, _, _ := net.SplitHostPort(gatewayAddr)
	pathBytes := make([]byte, 18)
	rand.Read(pathBytes)
	return &receiver{
		gatewayHost: host,
		aesKey:      key,
		aesIV:       iv,
		path:        "/" + base64.URLEncoding.EncodeToString(pathBytes),
		result:      make(chan recvResult, 1),
	}
}

func (r *receiver) Start(bindIP string) error {
	mux := http.NewServeMux()
	mux.HandleFunc(r.path, r.handleUpload)
	r.server = &http.Server{Handler: mux}

	var err error
	r.listener, err = net.Listen("tcp", net.JoinHostPort(bindIP, "0"))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	go r.server.Serve(r.listener)
	return nil
}

func (r *receiver) URL() string {
	return fmt.Sprintf("http://%s%s", r.listener.Addr(), r.path)
}

func (r *receiver) Close() {
	if r.server != nil {
		r.server.Close()
	}
}

func (r *receiver) handleUpload(w http.ResponseWriter, req *http.Request) {
	send := func(key string, err error) {
		r.once.Do(func() { r.result <- recvResult{key, err} })
	}

	if req.Method != http.MethodPut {
		http.Error(w, "method not allowed", 405)
		return
	}
	remoteHost, _, _ := net.SplitHostPort(req.RemoteAddr)
	if remoteHost != r.gatewayHost {
		http.Error(w, "forbidden", 403)
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		http.Error(w, "read error", 400)
		send("", fmt.Errorf("read upload: %w", err))
		return
	}
	w.WriteHeader(200)

	key, err := parseEncryptedConf(body, r.aesKey, r.aesIV)
	send(key, err)
}

// --- decrypt + parse --------------------------------------------------------

func parseEncryptedConf(ciphertext, key, iv []byte) (string, error) {
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return "", fmt.Errorf("bad ciphertext length %d", len(ciphertext))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ciphertext)

	// PKCS7 unpad
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > aes.BlockSize {
		return "", fmt.Errorf("bad PKCS7 padding (%d)", pad)
	}
	for _, b := range plain[len(plain)-pad:] {
		if int(b) != pad {
			return "", fmt.Errorf("inconsistent PKCS7 padding")
		}
	}
	plain = plain[:len(plain)-pad]

	tr := tar.NewReader(bytes.NewReader(plain))
	hdr, err := tr.Next()
	if err != nil {
		return "", fmt.Errorf("read tar: %w", err)
	}
	if !strings.HasSuffix(hdr.Name, "device.conf") {
		return "", fmt.Errorf("unexpected tar entry: %s", hdr.Name)
	}
	content, err := io.ReadAll(io.LimitReader(tr, 64<<10))
	if err != nil {
		return "", fmt.Errorf("read device.conf from tar: %w", err)
	}

	var gwKey string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, "="); ok && k == "key" {
			gwKey = v
		}
	}
	if len(gwKey) != 16 {
		return "", fmt.Errorf("gateway key not found or wrong length (%d)", len(gwKey))
	}
	return gwKey, nil
}

func localIPFor(remoteAddr string) (string, error) {
	conn, err := net.Dial("udp", remoteAddr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}
