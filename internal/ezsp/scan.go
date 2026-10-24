package ezsp

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"time"
)

// EZSP frame IDs.
const (
	frameVersion              = 0x0000
	frameNetworkInit          = 0x0017
	frameNetworkState         = 0x0018
	frameGetEui64             = 0x0026
	frameGetNodeID            = 0x0027
	frameGetNetworkParameters = 0x0028
	frameNeighborCount        = 0x007A
	frameGetNeighbor          = 0x0079
)

type NetworkInfo struct {
	Status       string `json:"status"`
	Channel      uint8  `json:"channel"`
	PanID        uint16 `json:"pan_id"`
	ExtPanID     string `json:"ext_pan_id"`
	NodeID       uint16 `json:"node_id"`
	EUI64        string `json:"eui64"`
	EZSPVersion  uint8  `json:"ezsp_version"`
	StackVersion string `json:"stack_version"`
}

type Device struct {
	EUI64    string `json:"eui64"`
	ShortID  uint16 `json:"short_id"`
	LQI      uint8  `json:"lqi"`
	RSSI     int8   `json:"rssi,omitempty"`
	InCost   uint8  `json:"in_cost,omitempty"`
	OutCost  uint8  `json:"out_cost,omitempty"`
	Age      uint8  `json:"age,omitempty"`
	LastSeen string `json:"last_seen,omitempty"`
}

type ScanResult struct {
	Network   NetworkInfo `json:"network"`
	Devices   []Device    `json:"devices"`
	ScannedAt string      `json:"scanned_at"`
}

// Scan connects to the NCP, queries network info and the neighbor table.
func Scan(rw io.ReadWriter) (*ScanResult, error) {
	c := NewConn(rw)
	if err := c.Reset(); err != nil {
		return nil, fmt.Errorf("ASH reset: %w", err)
	}

	result := &ScanResult{ScannedAt: time.Now().UTC().Format(time.RFC3339)}

	ver, vresp, err := negotiateVersion(c)
	if err != nil {
		return nil, err
	}
	result.Network.EZSPVersion = ver
	if len(vresp) >= 4 {
		stackVer := binary.LittleEndian.Uint16(vresp[2:4])
		result.Network.StackVersion = fmt.Sprintf("%d.%d.%d.%d",
			(stackVer>>12)&0xF, (stackVer>>8)&0xF, (stackVer>>4)&0xF, stackVer&0xF)
	}

	// Resume the network from NV storage (version command ceases network ops).
	resp, err := c.Command(frameNetworkInit, 0x00, 0x00)
	if err == nil && len(resp) >= 1 {
		if resp[0] == 0x00 {
			log.Printf("scan: networkInit OK, waiting for stack")
			time.Sleep(2 * time.Second)
		} else {
			log.Printf("scan: networkInit status=0x%02X", resp[0])
		}
	} else if err != nil {
		log.Printf("scan: networkInit: %v", err)
	}

	resp, err = c.Command(frameGetEui64)
	if err == nil && len(resp) >= 8 {
		result.Network.EUI64 = formatEUI64(resp[:8])
	}

	resp, err = c.Command(frameGetNodeID)
	if err == nil && len(resp) >= 2 {
		result.Network.NodeID = binary.LittleEndian.Uint16(resp[:2])
	}

	resp, err = c.Command(frameNetworkState)
	if err == nil && len(resp) >= 1 {
		switch resp[0] {
		case 0:
			result.Network.Status = "no-network"
		case 1:
			result.Network.Status = "joining"
		case 2:
			result.Network.Status = "joined"
		case 3:
			result.Network.Status = "joined-no-parent"
		case 4:
			result.Network.Status = "leaving"
		default:
			result.Network.Status = fmt.Sprintf("unknown(%d)", resp[0])
		}
	}

	resp, err = c.Command(frameGetNetworkParameters)
	if err == nil && len(resp) >= 22 {
		if resp[0] == 0x00 {
			result.Network.ExtPanID = formatEUI64(resp[2:10])
			result.Network.PanID = binary.LittleEndian.Uint16(resp[10:12])
			result.Network.Channel = resp[13]
		}
	}

	resp, err = c.Command(frameNeighborCount)
	if err == nil && len(resp) >= 1 {
		count := int(resp[0])
		for i := 0; i < count; i++ {
			resp, err = c.Command(frameGetNeighbor, byte(i))
			if err != nil {
				break
			}
			if len(resp) < 15 || resp[0] != 0x00 {
				continue
			}
			dev := Device{
				ShortID: binary.LittleEndian.Uint16(resp[1:3]),
				LQI:     resp[3],
				InCost:  resp[4],
				OutCost: resp[5],
				Age:     resp[6],
				EUI64:   formatEUI64(resp[7:15]),
			}
			result.Devices = append(result.Devices, dev)
		}
	}

	return result, nil
}

func negotiateVersion(c *Conn) (uint8, []byte, error) {
	c.EzspV8 = false
	resp, err := c.Command(frameVersion, 7)
	if err != nil {
		return 0, nil, fmt.Errorf("version: %w", err)
	}
	if len(resp) < 1 {
		return 0, nil, fmt.Errorf("empty version response")
	}
	ver := resp[0]
	log.Printf("scan: EZSP v%d negotiated", ver)
	if ver >= 8 {
		c.EzspV8 = true
		log.Printf("scan: switching to v8 extended frame format")
	}
	return ver, resp, nil
}

func (r *ScanResult) JSON() []byte {
	b, _ := json.MarshalIndent(r, "", "  ")
	return b
}

func formatEUI64(b []byte) string {
	if len(b) < 8 {
		return ""
	}
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x:%02x:%02x",
		b[7], b[6], b[5], b[4], b[3], b[2], b[1], b[0])
}
