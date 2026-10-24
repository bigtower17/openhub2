//go:build linux

// Package serial opens a raw 8N1 UART on Linux.
package serial

import (
	"fmt"

	"golang.org/x/sys/unix"
)

var speeds = map[int]uint32{
	57600:   unix.B57600,
	115200:  unix.B115200,
	230400:  unix.B230400,
	460800:  unix.B460800,
	921600:  unix.B921600,
	1000000: unix.B1000000,
	1500000: unix.B1500000,
	2000000: unix.B2000000,
}

// Port is a raw serial port. Reads return after at most 100 ms without data.
type Port struct{ fd int }

// Open configures path as raw 8N1 at baud. With rtscts, writes can block
// until the peer raises CTS.
func Open(path string, baud int, rtscts bool) (*Port, error) {
	speed, ok := speeds[baud]
	if !ok {
		return nil, fmt.Errorf("unsupported baud rate %d", baud)
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.CSTOPB | unix.CBAUD | unix.CRTSCTS
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL | speed
	if rtscts {
		t.Cflag |= unix.CRTSCTS
	}
	t.Ispeed, t.Ospeed = speed, speed
	t.Cc[unix.VMIN] = 0
	t.Cc[unix.VTIME] = 1
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, t); err != nil {
		unix.Close(fd)
		return nil, err
	}
	_ = unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIOFLUSH)
	return &Port{fd: fd}, nil
}

// Read returns 0, nil when no data arrived within the read timeout.
func (p *Port) Read(b []byte) (int, error) {
	for {
		n, err := unix.Read(p.fd, b)
		if err == unix.EINTR {
			continue
		}
		if n < 0 {
			n = 0
		}
		return n, err
	}
}

func (p *Port) Write(b []byte) (int, error) {
	total := 0
	for total < len(b) {
		n, err := unix.Write(p.fd, b[total:])
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func (p *Port) Close() error {
	if p.fd < 0 {
		return nil
	}
	err := unix.Close(p.fd)
	p.fd = -1
	return err
}
