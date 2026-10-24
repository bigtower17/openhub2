package ezsp

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// EZSP frame IDs (v7).
const (
	frameVersion              = 0x00
	frameNetworkState         = 0x18
	frameGetEui64             = 0x26
	frameGetNodeID            = 0x27
	frameGetNetworkParameters = 0x28
	frameNeighborCount        = 0x7A
	frameGetNeighbor          = 0x79
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
	EUI64   string `json:"eui64"`
	ShortID uint16 `json:"short_id"`
	LQI     uint8  `json:"lqi"`
	InCost  uint8  `json:"in_cost"`
	OutCost uint8  `json:"out_cost"`
	Age     uint8  `json:"age"`
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

	resp, err := c.Command(frameVersion, 7)
	if err != nil {
		return nil, fmt.Errorf("version: %w", err)
	}
	if len(resp) >= 4 {
		result.Network.EZSPVersion = resp[0]
		stackVer := binary.LittleEndian.Uint16(resp[2:4])
		result.Network.StackVersion = fmt.Sprintf("%d.%d.%d.%d",
			(stackVer>>12)&0xF, (stackVer>>8)&0xF, (stackVer>>4)&0xF, stackVer&0xF)
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
		if resp[0] == 0x00 { // EMBER_SUCCESS
			// resp[1] = nodeType
			result.Network.ExtPanID = formatEUI64(resp[2:10])
			result.Network.PanID = binary.LittleEndian.Uint16(resp[10:12])
			// resp[12] = txPower
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
