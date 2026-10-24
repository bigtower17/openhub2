package h5

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func TestEncodeSync(t *testing.T) {
	got := Encode(TypeLink, false, 0, 0, msgSync)
	want := []byte{0xc0, 0x00, 0x2f, 0x00, 0xd0, 0x01, 0x7e, 0xc0}
	if !bytes.Equal(got, want) {
		t.Fatalf("got % x want % x", got, want)
	}
}

func TestSlipRoundTrip(t *testing.T) {
	data := []byte{0x01, 0xc0, 0xdb, 0x02}
	f := Encode(TypeEvent, true, 3, 5, data)
	if bytes.Count(f, []byte{0xc0}) != 2 {
		t.Fatalf("unescaped delimiter in % x", f)
	}
	raw := unslip(f[1 : len(f)-1])
	if !bytes.Equal(raw[4:], data) {
		t.Fatalf("payload % x", raw[4:])
	}
}

// fakeController answers the H5 handshake and acknowledges reliable packets,
// replying to each HCI command with a Command Complete event.
type fakeController struct {
	toHost   *io.PipeWriter
	fromHost *io.PipeReader
	seq, ack uint8
}

func (f *fakeController) run(t *testing.T) {
	buf := make([]byte, 256)
	var frame []byte
	for {
		n, err := f.fromHost.Read(buf)
		if err != nil {
			return
		}
		for _, b := range buf[:n] {
			if b != 0xc0 {
				frame = append(frame, b)
				continue
			}
			if len(frame) >= 4 {
				f.handle(unslip(frame))
			}
			frame = frame[:0]
		}
	}
}

func (f *fakeController) send(typ uint8, reliable bool, payload []byte) {
	seq := uint8(0)
	if reliable {
		seq = f.seq
		f.seq = (f.seq + 1) & 7
	}
	f.toHost.Write(Encode(typ, reliable, seq, f.ack, payload))
}

func (f *fakeController) handle(p []byte) {
	typ := p[1] & 0x0f
	payload := p[4:]
	switch {
	case typ == TypeLink && bytes.HasPrefix(payload, msgSync):
		f.send(TypeLink, false, msgSyncRsp)
	case typ == TypeLink && bytes.HasPrefix(payload, msgConf):
		f.send(TypeLink, false, []byte{0x04, 0x7b, 0x01})
	case typ == TypeCommand:
		f.ack = (p[0] + 1) & 7
		// Command Complete: 1 credit, echoed opcode, status 0
		f.send(TypeEvent, true, []byte{0x0e, 0x04, 0x01, payload[0], payload[1], 0x00})
	}
}

type duplex struct {
	io.Reader
	io.Writer
}

func TestHandshakeAndCommand(t *testing.T) {
	hr, cw := io.Pipe()
	cr, hw := io.Pipe()
	fc := &fakeController{toHost: cw, fromHost: cr}
	go fc.run(t)

	l := New(duplex{hr, hw})
	go l.Run()
	if err := l.Establish(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ { // wraps the 3-bit sequence numbers
		if err := l.Send(TypeCommand, []byte{0x03, 0x0c, 0x00}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		select {
		case p := <-l.Packets:
			if p.Type != TypeEvent || p.Payload[0] != 0x0e {
				t.Fatalf("unexpected packet %+v", p)
			}
		case <-time.After(time.Second):
			t.Fatalf("no event for command %d", i)
		}
	}
}
