// ohinstall configures a Xiaomi Smart Home Hub 2 and installs OpenHub2
// without the Xiaomi Mi Home app. It talks to the hub over the setup
// access point (miio UDP 54321) to read the device token, configure
// Wi-Fi, open a root shell and deploy the OpenHub2 files.
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
	"bufio"
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
	gwKey := flag.String("key", "", "16-char gateway key (from XiaomiGateway3 or Xiaomi cloud)")
	flag.Parse()

	log.SetFlags(0)

	// --- step 1: discover the hub on the setup AP ---------------------------
	log.Println("[1/5] Discovering hub on the setup AP...")
	dr, err := miio.Discover(setupHost)
	if err != nil {
		log.Fatalf("Cannot reach the hub at %s.\n"+
			"Make sure you are connected to the hub's AP (lumi-gateway-mgl001_mibt…)\n"+
			"and the hub LED is blinking blue.\nError: %v", setupHost, err)
	}
	if dr.TokenHex == "" {
		log.Fatal("Hub responded but the token is hidden (all-ff).\n" +
			"The hub is already provisioned. Run a network reset first:\n" +
			"hold the button ~10 s until the LED blinks orange, then wait for blue.")
	}
	log.Printf("    Device ID : %d", dr.DeviceID)
	log.Printf("    Token     : %s", dr.TokenHex)

	// --- step 2: get device info --------------------------------------------
	log.Println("[2/5] Reading device info...")
	dev, err := miio.New(setupHost, dr.TokenHex)
	if err != nil {
		log.Fatalf("Failed to create miio client: %v", err)
	}
	info, err := dev.Info()
	if err != nil {
		log.Fatalf("miIO.info failed: %v", err)
	}
	log.Printf("    Model     : %s", info.Model)
	log.Printf("    Firmware  : %s", info.FwVer)
	log.Printf("    MAC       : %s", info.Mac)

	if info.Model != "lumi.gateway.mgl001" {
		log.Printf("WARNING: expected lumi.gateway.mgl001, got %s. Proceeding anyway.", info.Model)
	}

	// --- step 3: configure Wi-Fi --------------------------------------------
	if *ssid != "" {
		log.Printf("[3/5] Configuring Wi-Fi (%s)...", *ssid)
		_, err := dev.Call("miIO.config_router", map[string]any{
			"ssid":   *ssid,
			"passwd": *password,
			"uid":    0,
		})
		if err != nil {
			log.Printf("    miIO.config_router failed: %v", err)
			log.Println("    The hub may not support this method in the current state.")
			log.Println("    You can configure Wi-Fi later via SSH or the web panel.")
		} else {
			log.Println("    Wi-Fi credentials sent. The hub will join the network shortly.")
		}
	} else {
		log.Println("[3/5] No -ssid given, skipping Wi-Fi configuration.")
	}

	// --- step 4: open root shell via system_command -------------------------
	if *gwKey == "" {
		log.Println("[4/5] No -key given. Cannot open root shell automatically.")
		log.Println()
		log.Println("To get the gateway key, add the hub to Mi Home temporarily and use")
		log.Println("the XiaomiGateway3 integration (https://github.com/AlexxIT/XiaomiGateway3)")
		log.Println("or read it from the Xiaomi cloud. Then re-run with -key <16-char-key>.")
		log.Println()
		log.Println("If you already have SSH/telnet access, you can deploy manually:")
		log.Println("  see README.md § Install")
		printSummary(dr, info)
		return
	}

	log.Println("[4/5] Opening root shell via miio system_command...")
	err = dev.SystemCommand(info.Mac, *gwKey, "passwd -d root && /etc/init.d/dropbear start")
	if err != nil {
		log.Printf("    system_command failed: %v", err)
		log.Println("    The gateway key may be wrong, or the hub doesn't accept")
		log.Println("    system_command in this state.")
		printSummary(dr, info)
		os.Exit(1)
	}
	log.Println("    Root shell opened (telnet/dropbear).")

	// --- step 5: deploy files -----------------------------------------------
	log.Println("[5/5] Deployment")
	log.Println()
	log.Println("Root access is open. Connect via telnet or SSH to the hub and")
	log.Println("follow the install steps in README.md, or use the deploy helper:")
	log.Println()
	if *ssid != "" {
		log.Println("    Wait for the hub to join your Wi-Fi, then:")
	}
	log.Printf("    1. Copy build/gatessh   → /data/gatessh/gatessh")
	log.Printf("    2. Copy build/zbbridge  → /data/openhub2/zbbridge")
	log.Printf("    3. Copy build/ohweb     → /data/openhub2/ohweb")
	log.Printf("    4. Copy hub/data/openhub2/* → /data/openhub2/")
	log.Printf("    5. Copy hub/data/scripts/post_init.sh → /data/scripts/post_init.sh")
	log.Printf("    6. chmod +x all of the above")
	log.Printf("    7. /data/gatessh/gatessh -set-password (set your SSH password)")
	log.Printf("    8. /data/openhub2/ohctl mode openhub2  (reboots into OpenHub2)")
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
	log.Println("Save the token — you will need it if you want to reconfigure the hub later.")
}

func prompt(msg string) string {
	fmt.Print(msg)
	s := bufio.NewScanner(os.Stdin)
	s.Scan()
	return strings.TrimSpace(s.Text())
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
