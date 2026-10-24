// ohinstall configures a Xiaomi Smart Home Hub 2 and installs OpenHub2
// without the Xiaomi Mi Home app or cloud account. It talks to the hub
// over the setup access point (miio UDP 54321) to read the device token,
// recover the gateway key over LAN, open a root shell and configure Wi-Fi.
//
// Usage:
//
//	ohinstall -ssid MyNetwork -password secret [-key GATEWAYKEY]
//
// The user must first trigger a network reset on the hub (hold the
// button ~10 s until the LED blinks orange, then wait for blue blink)
// and connect their PC to the hub's open AP (lumi-gateway-mgl001_mibt…).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/bigtower17/openhub2/internal/miio"
)

const setupHost = "192.168.49.1"

func main() {
	ssid := flag.String("ssid", "", "Wi-Fi SSID to configure on the hub")
	password := flag.String("password", "", "Wi-Fi password (empty for open network)")
	gwKey := flag.String("key", "", "16-char gateway key (skip automatic recovery)")
	host := flag.String("host", setupHost, "hub IP (default: setup AP address)")
	flag.Parse()

	log.SetFlags(0)

	// --- step 1: discover ---------------------------------------------------
	log.Println("[1/5] Discovering hub...")
	dr, err := miio.Discover(*host)
	if err != nil {
		log.Fatalf("Cannot reach the hub at %s.\n"+
			"Make sure you are connected to the hub's AP (lumi-gateway-mgl001_mibt…)\n"+
			"and the hub LED is blinking blue.\nError: %v", *host, err)
	}
	if dr.TokenHex == "" {
		log.Fatal("Hub responded but the token is hidden (all-ff).\n" +
			"The hub is already provisioned. Run a network reset first:\n" +
			"hold the button ~10 s until the LED blinks orange, then wait for blue.")
	}
	log.Printf("    Device ID : %d", dr.DeviceID)
	log.Printf("    Token     : %s", dr.TokenHex)

	// --- step 2: device info ------------------------------------------------
	log.Println("[2/5] Reading device info...")
	dev, err := miio.New(*host, dr.TokenHex)
	if err != nil {
		log.Fatalf("miio client: %v", err)
	}
	info, err := dev.Info()
	if err != nil {
		log.Fatalf("miIO.info: %v", err)
	}
	log.Printf("    Model     : %s", info.Model)
	log.Printf("    Firmware  : %s", info.FwVer)
	log.Printf("    MAC       : %s", info.Mac)

	if info.Model != "lumi.gateway.mgl001" {
		log.Printf("WARNING: expected lumi.gateway.mgl001, got %s", info.Model)
	}

	// --- step 3: recover gateway key ----------------------------------------
	key := *gwKey
	if key == "" {
		log.Println("[3/5] Recovering gateway key over LAN...")
		log.Println("    Requesting diagnostic upload of /data/miio/device.conf...")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		recovered, err := dev.RecoverKey(ctx, "")
		cancel()
		if err != nil {
			log.Printf("    Key recovery failed: %v", err)
			log.Println("    You can provide the key manually with -key <16-char-key>.")
			log.Println("    To find it: SSH into the hub and run:")
			log.Println("      cat /data/miio/device.conf")
			printSummary(dr, info)
			os.Exit(1)
		}
		key = recovered
		log.Printf("    Gateway key recovered: %s", key)
	} else {
		log.Printf("[3/5] Using provided gateway key: %s", key)
	}

	// --- step 4: open root shell --------------------------------------------
	log.Println("[4/5] Opening root shell via system_command...")
	err = dev.SystemCommand(info.Mac, key, "passwd -d root && /etc/init.d/dropbear start")
	if err != nil {
		log.Printf("    system_command failed: %v", err)
		log.Println("    The gateway key may be wrong, or the hub doesn't")
		log.Println("    accept system_command in this firmware.")
		printSummary(dr, info)
		os.Exit(1)
	}
	log.Println("    Root shell opened (dropbear/telnet).")

	// --- step 5: configure Wi-Fi --------------------------------------------
	if *ssid != "" {
		log.Printf("[5/5] Configuring Wi-Fi (%s)...", *ssid)
		_, err := dev.Call("miIO.config_router", map[string]any{
			"ssid":   *ssid,
			"passwd": *password,
			"uid":    0,
		})
		if err != nil {
			log.Printf("    miIO.config_router: %v", err)
			log.Println("    Configure Wi-Fi later via SSH:")
			log.Println("      /bin/reset_wifi_info.sh <ssid> <password>")
		} else {
			log.Println("    Wi-Fi credentials sent.")
		}
	} else {
		log.Println("[5/5] No -ssid given, skipping Wi-Fi configuration.")
		log.Println("    Configure Wi-Fi via SSH:")
		log.Println("      /bin/reset_wifi_info.sh <ssid> <password>")
	}

	log.Println()
	log.Println("Root access is open. Deploy OpenHub2 files:")
	log.Println()
	if *ssid != "" {
		log.Println("    Wait for the hub to join your Wi-Fi, then connect via")
		log.Println("    telnet to the hub's new IP (check your router).")
	} else {
		fmt.Println("    Connect via telnet to " + *host)
	}
	log.Println()
	log.Println("    1. Copy build/gatessh   → /data/gatessh/gatessh")
	log.Println("    2. Copy build/zbbridge  → /data/openhub2/zbbridge")
	log.Println("    3. Copy build/ohweb     → /data/openhub2/ohweb")
	log.Println("    4. Copy hub/data/openhub2/* → /data/openhub2/")
	log.Println("    5. Copy hub/data/scripts/post_init.sh → /data/scripts/post_init.sh")
	log.Println("    6. chmod +x all of the above")
	log.Println("    7. /data/gatessh/gatessh -set-password")
	log.Println("    8. /data/openhub2/ohctl mode openhub2  (reboots)")
	log.Println()
	printSummary(dr, info)
}

func printSummary(dr *miio.DiscoverResult, info *miio.Info) {
	log.Println("─── Summary ───")
	log.Printf("  Device ID : %d", dr.DeviceID)
	log.Printf("  Token     : %s", dr.TokenHex)
	log.Printf("  Model     : %s", info.Model)
	log.Printf("  MAC       : %s", info.Mac)
	log.Printf("  Firmware  : %s", info.FwVer)
	log.Println()
	log.Println("Save the token — you will need it to reconfigure the hub later.")
}

func prompt(msg string) string {
	fmt.Print(msg)
	var line string
	fmt.Scanln(&line)
	return strings.TrimSpace(line)
}

func waitForHub(host string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := miio.Discover(host); err == nil {
			return true
		}
		time.Sleep(3 * time.Second)
	}
	return false
}
