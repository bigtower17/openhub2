// ohbutton watches the hub's physical button (input device "main-key") in
// the OpenHub2 userland: three quick presses make the next boot the stock
// Xiaomi system and reboot.
package main

import (
	"encoding/binary"
	"flag"
	"log"
	"os"
	"os/exec"
	"time"
)

const (
	evKey       = 1
	switchFlag  = "/tmp/openhub2-switch-stock"
	disableFlag = "/data/openhub2/userland.disabled"
)

var (
	device = flag.String("device", "/dev/input/event0", "button input device")
	clicks = flag.Int("clicks", 3, "presses that switch to the stock system")
	gap    = flag.Duration("gap", 1500*time.Millisecond, "maximum time between presses")
)

func main() {
	flag.Parse()
	f, err := os.Open(*device)
	if err != nil {
		log.Fatal(err)
	}
	// struct input_event on 32-bit ARM: timeval (2x int32), type u16, code u16, value s32
	ev := make([]byte, 16)
	var count int
	var last time.Time
	for {
		if _, err := f.Read(ev); err != nil {
			log.Fatal(err)
		}
		typ := binary.LittleEndian.Uint16(ev[8:10])
		value := int32(binary.LittleEndian.Uint32(ev[12:16]))
		if typ != evKey || value != 1 {
			continue
		}
		now := time.Now()
		if now.Sub(last) > *gap {
			count = 0
		}
		last = now
		count++
		log.Printf("button press %d/%d", count, *clicks)
		if count >= *clicks {
			switchToStock()
			count = 0
		}
	}
}

func switchToStock() {
	log.Printf("switching to the stock system")
	os.WriteFile(switchFlag, nil, 0o644)
	if _, err := os.Stat("/data/openhub2"); err == nil {
		os.WriteFile(disableFlag, nil, 0o644)
	}
	run("ohled", "orange", "blink")
	if err := run("fw_setenv", "oh_next", "run", "oh_stock"); err != nil {
		log.Printf("fw_setenv: %v", err)
	}
	run("sync")
	time.Sleep(2 * time.Second)
	run("reboot")
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		log.Printf("%s: %v %s", name, err, out)
	}
	return err
}
