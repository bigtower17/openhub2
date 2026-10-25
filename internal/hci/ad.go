package hci

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Well-known IDs only; anything else is shown as a hex ID.
var companies = map[uint16]string{0x004c: "Apple", 0x0006: "Microsoft", 0x0075: "Samsung", 0x00e0: "Google"}
var services = map[uint16]string{0xfe95: "Xiaomi MiBeacon", 0xfcd2: "BTHome", 0x181a: "Environmental Sensing"}

// ParseAD extracts the local name, manufacturer and 16-bit service data
// UUIDs from advertising data (Bluetooth Core Supplement, Part A).
func ParseAD(data []byte) (name, mfr, svc string) {
	var svcs []string
	for len(data) >= 2 {
		l := int(data[0])
		if l == 0 || l+1 > len(data) {
			break
		}
		typ, v := data[1], data[2:l+1]
		switch typ {
		case 0x08, 0x09: // shortened / complete local name
			name = strings.ToValidUTF8(string(v), "?")
		case 0xff:
			if len(v) >= 2 {
				id := binary.LittleEndian.Uint16(v)
				if n, ok := companies[id]; ok {
					mfr = n
				} else {
					mfr = fmt.Sprintf("0x%04X", id)
				}
			}
		case 0x16: // service data, 16-bit UUID
			if len(v) >= 2 {
				id := binary.LittleEndian.Uint16(v)
				if n, ok := services[id]; ok {
					svcs = append(svcs, n)
				} else {
					svcs = append(svcs, fmt.Sprintf("0x%04X", id))
				}
			}
		}
		data = data[l+1:]
	}
	return name, mfr, strings.Join(svcs, ", ")
}
