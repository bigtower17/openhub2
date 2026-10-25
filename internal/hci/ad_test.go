package hci

import "testing"

func TestParseAD(t *testing.T) {
	// flags, complete name "LYWSD03MMC", service data 0xFE95, manufacturer Apple
	data := []byte{2, 0x01, 0x06, 11, 0x09, 'L', 'Y', 'W', 'S', 'D', '0', '3', 'M', 'M', 'C', 4, 0x16, 0x95, 0xfe, 0x30, 3, 0xff, 0x4c, 0x00}
	name, mfr, svc := ParseAD(data)
	if name != "LYWSD03MMC" || mfr != "Apple" || svc != "Xiaomi MiBeacon" {
		t.Fatalf("%q %q %q", name, mfr, svc)
	}
	// truncated structure must not panic
	ParseAD([]byte{5, 0x09, 'a'})
}
