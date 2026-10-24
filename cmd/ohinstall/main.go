// ohinstall configures a Xiaomi Smart Home Hub 2 and installs OpenHub2
// without the Xiaomi Mi Home app or cloud account. It talks to the hub
// over the setup access point (miio UDP 54321) to read the device token,
// recover the gateway key over LAN, open a root shell, deploy all files
// via SSH and configure Wi-Fi.
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
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/bigtower17/openhub2/internal/deploy"
	"github.com/bigtower17/openhub2/internal/miio"
)

const setupHost = "192.168.49.1"

func main() {
	ssid := flag.String("ssid", "", "Wi-Fi SSID to configure on the hub")
	password := flag.String("password", "", "Wi-Fi password (empty for open network)")
	gwKey := flag.String("key", "", "16-char gateway key (skip automatic recovery)")
	host := flag.String("host", setupHost, "hub IP (default: setup AP address)")
	sshPort := flag.Int("ssh-port", 22, "SSH port on the hub")
	buildDir := flag.String("build", "build", "directory with compiled ARM binaries")
	hubDir := flag.String("hub", "hub", "directory with hub data files")
	noDeploy := flag.Bool("no-deploy", false, "skip file deployment (print instructions instead)")
	flag.Parse()

	log.SetFlags(0)

	// --- step 1: discover ---------------------------------------------------
	log.Println("[1/7] Discovering hub...")
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
	log.Println("[2/7] Reading device info...")
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
		log.Println("[3/7] Recovering gateway key over LAN...")
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
		log.Printf("[3/7] Using provided gateway key: %s", key)
	}

	// --- step 4: open root shell --------------------------------------------
	log.Println("[4/7] Opening root shell via system_command...")
	err = dev.SystemCommand(info.Mac, key, "passwd -d root && /etc/init.d/dropbear start")
	if err != nil {
		log.Printf("    system_command failed: %v", err)
		log.Println("    The gateway key may be wrong, or the hub doesn't")
		log.Println("    accept system_command in this firmware.")
		printSummary(dr, info)
		os.Exit(1)
	}
	log.Println("    Root shell opened (dropbear started).")

	// --- step 5: deploy files -----------------------------------------------
	if *noDeploy {
		log.Println("[5/7] Skipping deploy (--no-deploy).")
		printManualDeploy()
	} else {
		log.Println("[5/7] Deploying files to hub via SSH...")

		files, err := buildFileList(*buildDir, *hubDir)
		if err != nil {
			log.Printf("    %v", err)
			log.Println("    Run 'make' first to build the ARM binaries.")
			printManualDeploy()
			os.Exit(1)
		}

		time.Sleep(2 * time.Second)

		if err := deploy.Run(*host, *sshPort, files); err != nil {
			log.Printf("    Deploy failed: %v", err)
			log.Println("    You can deploy manually after the hub joins Wi-Fi.")
			printManualDeploy()
		} else {
			log.Println("    All files deployed.")
		}
	}

	// --- step 6: set SSH password -------------------------------------------
	log.Println("[6/7] Setting SSH password...")
	sshPw := prompt("    Enter a password for SSH/web access (min 8 chars): ")
	if len(sshPw) < 8 {
		log.Println("    Password too short, skipping. Set it later on the hub:")
		log.Println("      /data/gatessh/gatessh -set-password")
	} else {
		hash, err := bcrypt.GenerateFromPassword([]byte(sshPw), bcrypt.DefaultCost)
		if err != nil {
			log.Printf("    bcrypt error: %v", err)
		} else if !*noDeploy {
			out, err := deploy.Exec(*host, *sshPort,
				fmt.Sprintf("mkdir -p /data/gatessh && cat > /data/gatessh/root.hash << 'HASH'\n%s\nHASH", string(hash)))
			if err != nil {
				log.Printf("    Failed to write hash: %v %s", err, out)
				log.Println("    Set it later: /data/gatessh/gatessh -set-password")
			} else {
				log.Println("    Password set.")
			}
		}
	}

	// --- step 7: configure Wi-Fi --------------------------------------------
	if *ssid != "" {
		log.Printf("[7/7] Configuring Wi-Fi (%s)...", *ssid)
		log.Println("    The hub will reboot and join your network.")
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
			log.Println("    Wi-Fi credentials sent. The hub is rebooting.")
		}
	} else {
		log.Println("[7/7] No -ssid given, skipping Wi-Fi configuration.")
		log.Println("    Configure Wi-Fi via SSH:")
		log.Println("      /bin/reset_wifi_info.sh <ssid> <password>")
	}

	log.Println()
	printSummary(dr, info)

	if *ssid != "" {
		log.Println()
		log.Println("The hub is joining your Wi-Fi. Next:")
		log.Println("  1. Reconnect your PC to your home network.")
		log.Println("  2. Find the hub's new IP (check your router, MAC: " + info.Mac + ").")
		log.Println("  3. In Home Assistant: Add integration → ZHA,")
		log.Println("     radio type EZSP, path socket://<hub-ip>:8888")
		log.Println("  4. Open http://<hub-ip>/ for the web panel.")
		log.Println("  5. Home Assistant discovers the Bluetooth proxy as an ESPHome device")
		log.Println("     (or add ESPHome manually: host <hub-ip>, port 6053, no encryption key).")
	}
}

func buildFileList(buildDir, hubDir string) ([]deploy.File, error) {
	files := []deploy.File{
		{Local: filepath.Join(buildDir, "gatessh"), Remote: "/data/gatessh/gatessh", Mode: 0o755},
		{Local: filepath.Join(buildDir, "zbbridge"), Remote: "/data/openhub2/zbbridge", Mode: 0o755},
		{Local: filepath.Join(buildDir, "ohweb"), Remote: "/data/openhub2/ohweb", Mode: 0o755},
		{Local: filepath.Join(buildDir, "btproxy"), Remote: "/data/openhub2/btproxy", Mode: 0o755},
		{Local: filepath.Join(hubDir, "data/openhub2/ohctl"), Remote: "/data/openhub2/ohctl", Mode: 0o755},
		{Local: filepath.Join(hubDir, "data/openhub2/prepare.sh"), Remote: "/data/openhub2/prepare.sh", Mode: 0o755},
		{Local: filepath.Join(hubDir, "data/scripts/post_init.sh"), Remote: "/data/scripts/post_init.sh", Mode: 0o755},
	}

	for _, f := range files {
		if _, err := os.Stat(f.Local); err != nil {
			return nil, fmt.Errorf("missing %s: build with 'make' first", f.Local)
		}
	}
	return files, nil
}

func printManualDeploy() {
	log.Println()
	log.Println("    Deploy manually:")
	log.Println("    1. Copy build/gatessh   → /data/gatessh/gatessh")
	log.Println("    2. Copy build/zbbridge  → /data/openhub2/zbbridge")
	log.Println("    3. Copy build/ohweb     → /data/openhub2/ohweb")
	log.Println("    4. Copy build/btproxy   → /data/openhub2/btproxy")
	log.Println("    5. Copy hub/data/openhub2/* → /data/openhub2/")
	log.Println("    6. Copy hub/data/scripts/post_init.sh → /data/scripts/post_init.sh")
	log.Println("    7. chmod +x all of the above")
	log.Println("    8. /data/gatessh/gatessh -set-password")
	log.Println("    9. /data/openhub2/ohctl mode openhub2  (reboots)")
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
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text())
	}
	return ""
}
