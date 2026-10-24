package esphome

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func readFrame(t *testing.T, r *bufio.Reader) (int, []byte) {
	t.Helper()
	if b, err := r.ReadByte(); err != nil || b != 0 {
		t.Fatalf("preamble %v %v", b, err)
	}
	n, _ := binary.ReadUvarint(r)
	typ, _ := binary.ReadUvarint(r)
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		t.Fatal(err)
	}
	return int(typ), p
}

func TestSessionAndAdverts(t *testing.T) {
	s := &Server{Info: Info{Name: "openhub2-516e", MAC: "54:EF:44:48:51:6E", Version: "2025.10.0"}}
	s.SetScannerState(true, false)
	srv, cli := net.Pipe()
	go s.handle(srv)
	cli.SetDeadline(time.Now().Add(3 * time.Second))
	r := bufio.NewReader(cli)

	cli.Write(frame(msgHelloRequest, pb(nil).str(1, "test").uint(2, 1).uint(3, 10)))
	if typ, p := readFrame(t, r); typ != msgHelloResponse {
		t.Fatalf("hello: type %d", typ)
	} else if f, _ := fields(p); f[1] != 1 || f[2] != apiVersionMinor {
		t.Fatalf("hello version %v", f)
	}

	cli.Write(frame(msgDeviceInfoRequest, nil))
	typ, p := readFrame(t, r)
	if typ != msgDeviceInfoResponse {
		t.Fatalf("device info: type %d", typ)
	}
	if f, _ := fields(p); f[15] != featurePassiveScan|featureRawAdvertising|featureStateAndMode {
		t.Fatalf("feature flags %v", f)
	}

	cli.Write(frame(msgSubscribeAdverts, pb(nil).uint(1, 1)))
	if typ, p := readFrame(t, r); typ != msgScannerState {
		t.Fatalf("scanner state: type %d", typ)
	} else if f, _ := fields(p); f[1] != scannerStateRunning {
		t.Fatalf("scanner state %v", f)
	}

	s.Publish([]Advertisement{{Address: [6]byte{0xa4, 0xc1, 0x38, 0x01, 0x02, 0x03}, RSSI: -60, Data: []byte{2, 1, 6}}})
	typ, p = readFrame(t, r)
	if typ != msgRawAdverts {
		t.Fatalf("adverts: type %d", typ)
	}
	// field 1 (embedded) → field 1 address varint
	if p[0] != 0x0a {
		t.Fatalf("adverts payload % x", p)
	}
	inner := p[2:]
	addr, n := binary.Uvarint(inner[1:])
	if inner[0] != 0x08 || addr != 0xa4c138010203 || n <= 0 {
		t.Fatalf("address % x", inner)
	}

	cli.Write(frame(msgPingRequest, nil))
	if typ, _ := readFrame(t, r); typ != msgPingResponse {
		t.Fatalf("ping: type %d", typ)
	}
}
