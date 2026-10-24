package esphome

import (
	"encoding/binary"
	"errors"
)

// Minimal protobuf encoding for the handful of ESPHome API messages we use.

type pb []byte

func (b pb) key(field, wire int) pb { return binary.AppendUvarint(b, uint64(field<<3|wire)) }

func (b pb) uint(field int, v uint64) pb {
	if v == 0 {
		return b
	}
	return binary.AppendUvarint(b.key(field, 0), v)
}

func (b pb) sint(field int, v int64) pb {
	return binary.AppendUvarint(b.key(field, 0), uint64(v<<1^v>>63))
}

func (b pb) bool(field int, v bool) pb {
	if !v {
		return b
	}
	return b.uint(field, 1)
}

func (b pb) bytes(field int, v []byte) pb {
	b = binary.AppendUvarint(b.key(field, 2), uint64(len(v)))
	return append(b, v...)
}

func (b pb) str(field int, v string) pb {
	if v == "" {
		return b
	}
	return b.bytes(field, []byte(v))
}

// fields decodes the varint fields of a message (enough for the requests
// we handle, which carry only integers).
func fields(msg []byte) (map[int]uint64, error) {
	out := map[int]uint64{}
	for len(msg) > 0 {
		k, n := binary.Uvarint(msg)
		if n <= 0 {
			return nil, errors.New("bad key")
		}
		msg = msg[n:]
		field, wire := int(k>>3), int(k&7)
		switch wire {
		case 0:
			v, n := binary.Uvarint(msg)
			if n <= 0 {
				return nil, errors.New("bad varint")
			}
			out[field] = v
			msg = msg[n:]
		case 1:
			if len(msg) < 8 {
				return nil, errors.New("short fixed64")
			}
			msg = msg[8:]
		case 2:
			l, n := binary.Uvarint(msg)
			if n <= 0 || uint64(len(msg)-n) < l {
				return nil, errors.New("bad length")
			}
			msg = msg[n+int(l):]
		case 5:
			if len(msg) < 4 {
				return nil, errors.New("short fixed32")
			}
			msg = msg[4:]
		default:
			return nil, errors.New("unsupported wire type")
		}
	}
	return out, nil
}

// frame wraps a message in the plaintext API framing:
// 0x00, varint(length), varint(type), payload.
func frame(typ int, payload []byte) []byte {
	out := []byte{0}
	out = binary.AppendUvarint(out, uint64(len(payload)))
	out = binary.AppendUvarint(out, uint64(typ))
	return append(out, payload...)
}
