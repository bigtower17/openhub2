package hci

import (
	"bytes"
	"testing"
)

func TestAdvReport(t *testing.T) {
	c := &Controller{Adverts: make(chan Advertisement, 4)}
	// one report: ADV_IND, public, address 03:02:01:38:C1:A4 (LSB first on the wire), 3 data bytes, RSSI -60
	c.leMeta(subLEAdvReport, []byte{1, 0x00, 0x00, 0x03, 0x02, 0x01, 0x38, 0xc1, 0xa4, 3, 2, 1, 6, 0xc4})
	a := <-c.Adverts
	if a.Address != [6]byte{0xa4, 0xc1, 0x38, 0x01, 0x02, 0x03} || a.RSSI != -60 || !bytes.Equal(a.Data, []byte{2, 1, 6}) {
		t.Fatalf("%+v", a)
	}
}
