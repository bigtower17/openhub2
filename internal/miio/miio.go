// Package miio implements the small subset of the Xiaomi miio LAN protocol
// (UDP 54321) needed to talk to the hub: handshake, encrypted JSON calls,
// and the signed "system_command" used to open telnet.
//
// The protocol is documented by python-miio; the system_command signature
// follows AlexxIT/XiaomiGateway3 (MIT).
package miio

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

type Device struct {
	addr     string
	token    []byte
	key, iv  []byte
	deviceID uint32
	stamp    uint32
	stampAt  time.Time
	nextID   int
}

func New(host, tokenHex string) (*Device, error) {
	token, err := hex.DecodeString(tokenHex)
	if err != nil || len(token) != 16 {
		return nil, errors.New("token must be 32 hex characters")
	}
	k := md5.Sum(token)
	iv := md5.Sum(append(k[:], token...))
	return &Device{addr: net.JoinHostPort(host, "54321"), token: token, key: k[:], iv: iv[:], nextID: 1}, nil
}

func (d *Device) DeviceID() uint32 { return d.deviceID }

func (d *Device) exchange(pkt []byte) ([]byte, error) {
	c, err := net.Dial("udp", d.addr)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if _, err := c.Write(pkt); err != nil {
		return nil, err
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4096)
	n, err := c.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// Hello performs the handshake and learns the device id and time stamp.
func (d *Device) Hello() error {
	pkt := make([]byte, 32)
	binary.BigEndian.PutUint16(pkt[0:], 0x2131)
	binary.BigEndian.PutUint16(pkt[2:], 32)
	for i := 4; i < 32; i++ {
		pkt[i] = 0xff
	}
	resp, err := d.exchange(pkt)
	if err != nil {
		return fmt.Errorf("miio hello: %w", err)
	}
	if len(resp) < 32 || binary.BigEndian.Uint16(resp) != 0x2131 {
		return errors.New("miio hello: bad response")
	}
	d.deviceID = binary.BigEndian.Uint32(resp[8:])
	d.stamp = binary.BigEndian.Uint32(resp[12:])
	d.stampAt = time.Now()
	return nil
}

// Call sends a method with params and returns the decoded "result".
func (d *Device) Call(method string, params any) (json.RawMessage, error) {
	if d.deviceID == 0 {
		if err := d.Hello(); err != nil {
			return nil, err
		}
	}
	if params == nil {
		params = []any{}
	}
	id := d.nextID
	d.nextID++
	body, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	enc, err := d.encrypt(body)
	if err != nil {
		return nil, err
	}
	pkt := make([]byte, 32+len(enc))
	binary.BigEndian.PutUint16(pkt[0:], 0x2131)
	binary.BigEndian.PutUint16(pkt[2:], uint16(len(pkt)))
	binary.BigEndian.PutUint32(pkt[8:], d.deviceID)
	binary.BigEndian.PutUint32(pkt[12:], d.stamp+uint32(time.Since(d.stampAt).Seconds())+1)
	copy(pkt[16:], d.token)
	copy(pkt[32:], enc)
	sum := md5.Sum(pkt)
	copy(pkt[16:], sum[:])

	resp, err := d.exchange(pkt)
	if err != nil {
		return nil, fmt.Errorf("miio %s: %w", method, err)
	}
	if len(resp) <= 32 {
		return nil, fmt.Errorf("miio %s: empty response (wrong token?)", method)
	}
	plain, err := d.decrypt(resp[32:])
	if err != nil {
		return nil, fmt.Errorf("miio %s: %w (wrong token?)", method, err)
	}
	plain = bytes.TrimRight(plain, "\x00")
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(plain, &r); err != nil {
		return nil, fmt.Errorf("miio %s: %w", method, err)
	}
	if r.Error != nil {
		return nil, fmt.Errorf("miio %s: error %d %s", method, r.Error.Code, r.Error.Message)
	}
	return r.Result, nil
}

type Info struct {
	Model string `json:"model"`
	FwVer string `json:"fw_ver"`
	Mac   string `json:"mac"`
}

func (d *Device) Info() (*Info, error) {
	raw, err := d.Call("miIO.info", nil)
	if err != nil {
		return nil, err
	}
	var i Info
	return &i, json.Unmarshal(raw, &i)
}

// SystemCommand runs a shell command on Multimode Gateway 2 firmware >= 1.0.7.
// The password is derived from the device id, MAC and the 16-char gateway key.
func (d *Device) SystemCommand(mac, gwKey, command string) error {
	if len(gwKey) != 16 {
		return errors.New("gateway key must be 16 characters")
	}
	secret := sha256.Sum256([]byte(fmt.Sprintf("%d%s%s", d.deviceID, mac, gwKey)))
	h := hmac.New(sha256.New, []byte(hex.EncodeToString(secret[:])))
	h.Write([]byte(gwKey))
	b64 := base64.StdEncoding.EncodeToString(h.Sum(nil))
	password := b64[len(b64)-16:]
	raw, err := d.Call("system_command", map[string]string{"password": password, "command": command})
	if err != nil {
		return err
	}
	if !bytes.Contains(raw, []byte(`"ok"`)) {
		return fmt.Errorf("system_command: unexpected result %s", raw)
	}
	return nil
}

func (d *Device) encrypt(b []byte) ([]byte, error) {
	blk, err := aes.NewCipher(d.key)
	if err != nil {
		return nil, err
	}
	pad := aes.BlockSize - len(b)%aes.BlockSize
	b = append(b, bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(b))
	cipher.NewCBCEncrypter(blk, d.iv).CryptBlocks(out, b)
	return out, nil
}

func (d *Device) decrypt(b []byte) ([]byte, error) {
	if len(b) == 0 || len(b)%aes.BlockSize != 0 {
		return nil, errors.New("bad ciphertext length")
	}
	blk, err := aes.NewCipher(d.key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(b))
	cipher.NewCBCDecrypter(blk, d.iv).CryptBlocks(out, b)
	pad := int(out[len(out)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(out) {
		return nil, errors.New("bad padding")
	}
	return out[:len(out)-pad], nil
}
