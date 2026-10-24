package ezsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func ncpFrame(frameID byte, params ...byte) []byte {
	ezsp := append([]byte{0x01, 0x80, 0xFF, 0x00, frameID}, params...)
	return encodeFrame(append([]byte{0x21}, randomize(ezsp)...), true)
}

func TestSnifferNetworkAndDevices(t *testing.T) {
	s := NewSniffer()
	var stream []byte
	stream = append(stream, ncpFrame(frameVersion, 7, 2, 0x60, 0x66)...)
	stream = append(stream, ncpFrame(frameGetEui64, 4, 0xcf, 0x8c, 0xdf, 0xee, 0x66, 0x19, 0xf1)...)
	params := make([]byte, 22)
	params[1] = 1 // coordinator
	copy(params[2:10], []byte{1, 2, 3, 4, 5, 6, 7, 8})
	params[10], params[11] = 0x34, 0x12
	params[13] = 15
	stream = append(stream, ncpFrame(frameGetNetworkParameters, params...)...)
	stream = append(stream, ncpFrame(frameStackStatusHandler, emberNetworkUp)...)

	// device announce from 0xABCD, LQI 200, RSSI -50
	announce := []byte{0x10, 0xCD, 0xAB, 8, 7, 6, 5, 4, 3, 2, 1, 0x80}
	msg := []byte{0x00, 0x00, 0x00, 0x13, 0x00, 0, 0, 0, 0, 0, 0, 0, 200, byte(0xCE), 0xCD, 0xAB, 0xFF, 0xFF, byte(len(announce))}
	stream = append(stream, ncpFrame(frameIncomingMessageHandler, append(msg, announce...)...)...)

	// feed in small chunks, as the UART delivers it
	for i := 0; i < len(stream); i += 3 {
		end := min(i+3, len(stream))
		s.feed(stream[i:end])
	}

	path := filepath.Join(t.TempDir(), "net.json")
	if err := s.Flush(path, false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var r ScanResult
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if r.Network.Status != "joined" || r.Network.Channel != 15 || r.Network.PanID != 0x1234 {
		t.Fatalf("network: %+v", r.Network)
	}
	if r.Network.EUI64 != "f1:19:66:ee:df:8c:cf:04" || r.Network.StackVersion != "6.6.6.0" {
		t.Fatalf("coordinator: %+v", r.Network)
	}
	if len(r.Devices) != 1 {
		t.Fatalf("devices: %+v", r.Devices)
	}
	d := r.Devices[0]
	if d.ShortID != 0xABCD || d.LQI != 200 || d.RSSI != -50 || d.EUI64 != "01:02:03:04:05:06:07:08" || d.LastSeen == "" {
		t.Fatalf("device: %+v", d)
	}
}
