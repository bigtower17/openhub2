// zbbridge exposes the hub's Zigbee radio (Silicon Labs EFR32 NCP, EZSP over
// ASH on a UART) on a TCP port, so ZHA or other EZSP hosts can use the hub as
// a network Zigbee coordinator (socket://<hub>:8888).
//
// The stock Zigbee host (mZ3GatewayHost_MQTT) must not be running: only one
// program can own the UART.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

var (
	addr     = flag.String("addr", ":8888", "TCP listen address")
	port     = flag.String("port", "/dev/ttyS1", "NCP serial port")
	baud     = flag.Int("baud", 115200, "NCP baud rate")
	rtscts   = flag.Bool("rtscts", true, "hardware flow control")
	resetPin = flag.Int("reset-gpio", 44, "GPIO driving the NCP reset line (active high), -1 to disable")
	probe    = flag.Bool("probe", false, "send an ASH reset, print the reply and exit")
)

func main() {
	flag.Parse()
	if *resetPin >= 0 {
		// hold the NCP out of reset (the stock host may leave it in reset)
		if err := gpioSet(*resetPin, 0); err != nil {
			log.Printf("reset gpio: %v", err)
		}
	}
	if *probe {
		b, err := probeNCP()
		fmt.Printf("reply % x err=%v\n", b, err)
		if err != nil {
			os.Exit(1)
		}
		return
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("listening on %s for %s @ %d", *addr, *port, *baud)
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		serve(c) // one client at a time: the NCP has a single host
	}
}

func serve(c net.Conn) {
	defer c.Close()
	log.Printf("client %s connected", c.RemoteAddr())
	ser, err := openSerial(*port, *baud, *rtscts)
	if err != nil {
		log.Printf("open %s: %v", *port, err)
		return
	}
	defer ser.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		io.Copy(c, ser) // NCP -> client
		c.Close()
	}()
	io.Copy(ser, c) // client -> NCP
	ser.Close()     // unblocks the reader goroutine
	wg.Wait()
	log.Printf("client %s disconnected", c.RemoteAddr())
}

// probeNCP sends an ASH RST frame and waits for RSTACK
// (UG101, "UART Gateway Protocol Reference").
func probeNCP() ([]byte, error) {
	ser, err := openSerial(*port, *baud, *rtscts)
	if err != nil {
		return nil, err
	}
	defer ser.Close()
	if _, err := ser.Write([]byte{0x1A, 0xC0, 0x38, 0xBC, 0x7E}); err != nil {
		return nil, err
	}
	rstack := []byte{0xC1, 0x02}
	buf := make([]byte, 0, 64)
	deadline := time.Now().Add(3 * time.Second)
	tmp := make([]byte, 64)
	for time.Now().Before(deadline) {
		n, err := ser.Read(tmp)
		if err != nil {
			return buf, err
		}
		buf = append(buf, tmp[:n]...)
		if i := bytes.Index(buf, rstack); i >= 0 && bytes.IndexByte(buf[i:], 0x7E) > 0 {
			return buf, nil
		}
	}
	return buf, errors.New("no RSTACK from NCP")
}

type serial struct{ fd int }

func (s *serial) Read(p []byte) (int, error) {
	for {
		n, err := unix.Read(s.fd, p)
		if err == unix.EINTR {
			continue
		}
		if n == 0 && err == nil {
			// VMIN=0/VTIME timeout: no data yet
			continue
		}
		if n < 0 {
			n = 0
		}
		return n, err
	}
}

func (s *serial) Write(p []byte) (int, error) { return unix.Write(s.fd, p) }

func (s *serial) Close() error {
	if s.fd < 0 {
		return nil
	}
	err := unix.Close(s.fd)
	s.fd = -1
	return err
}

func openSerial(path string, baudRate int, hw bool) (*serial, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	speed, ok := map[int]uint32{57600: unix.B57600, 115200: unix.B115200, 230400: unix.B230400, 460800: unix.B460800}[baudRate]
	if !ok {
		unix.Close(fd)
		return nil, fmt.Errorf("unsupported baud rate %d", baudRate)
	}
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	// raw 8N1
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.CSTOPB | unix.CBAUD | unix.CRTSCTS
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL | speed
	if hw {
		t.Cflag |= unix.CRTSCTS
	}
	t.Ispeed, t.Ospeed = speed, speed
	t.Cc[unix.VMIN] = 0
	t.Cc[unix.VTIME] = 10 // reads return after 1s without data
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, t); err != nil {
		unix.Close(fd)
		return nil, err
	}
	_ = unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIOFLUSH)
	return &serial{fd: fd}, nil
}

func gpioSet(pin, val int) error {
	path := fmt.Sprintf("/sys/class/gpio/gpio%d", pin)
	if _, err := os.Stat(path); err != nil {
		if err := os.WriteFile("/sys/class/gpio/export", []byte(fmt.Sprint(pin)), 0o200); err != nil {
			return err
		}
	}
	if err := os.WriteFile(path+"/direction", []byte("out"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(path+"/value", []byte(fmt.Sprint(val)), 0o644)
}
