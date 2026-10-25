# OpenHub2

Open tooling for the **Xiaomi Smart Home Hub 2** (`ZNDMWG04LM` / `BHR6765GL`,
`lumi.gateway.mgl001`). It turns the hub into a **network Zigbee coordinator
for Home Assistant (ZHA)** that works with **any Zigbee brand**, not only
Xiaomi/Aqara, and it keeps working without the Xiaomi cloud.

> Status: early, tested on one unit with stock firmware `1.0.7_0019`.
> Use at your own risk. Not affiliated with Xiaomi.

## What works

- **Root SSH** (`gatessh`): a small SSH server with shell, remote commands and
  SFTP, started at boot.
- **Network Zigbee coordinator** (`zbbridge`): exposes the hub's Silicon Labs
  EFR32 radio on TCP `:8888`. Point ZHA (or anything built on zigpy/bellows)
  at `socket://<hub-ip>:8888`.
  - Tested with Home Assistant 2026.10.3 (ZHA, bellows 1.1.0): a Tuya
    `TS004F` button paired through the hub, got its ZHA quirk applied and
    fired `zha_event`s for single/double/long presses.
- **Bluetooth proxy** (`btproxy`): turns the hub's Bluetooth LE radio into an
  [ESPHome Bluetooth proxy](https://esphome.io/components/bluetooth_proxy/).
  Home Assistant discovers it over mDNS as an ESPHome device (port `6053`,
  no encryption key) and receives every BLE advertisement the hub hears:
  Xiaomi, Govee, Switchbot, BTHome sensors and so on. Passive and active
  scanning; connections to BLE devices are not supported yet.
  - The controller is a Telink chip speaking HCI over the Three-wire UART
    (H5) protocol on `/dev/ttyS2` at 1 Mbaud, reset by GPIO 60.
- **Mode switch** (`ohctl`): `openhub2` hands the Zigbee radio to `zbbridge`
  and the Bluetooth radio to `btproxy`, while `xiaomi` keeps the stock Mi
  Home Zigbee and Bluetooth. Wi-Fi and the rest of the stock services keep
  running in both modes.
- **Web panel** (`ohweb`): a single-page dashboard on port 80, protected by
  the SSH password. Live tiles for system, CPU, memory, storage, network,
  Zigbee, Bluetooth and the Home Assistant connections (ZHA and the
  ESPHome proxy), 24 h charts kept in memory, Zigbee and Bluetooth device
  lists, Wi-Fi scan and join, system and mode switch, LED color, logs.
  Everything is embedded in one binary and works without internet.
- **OpenHub2 userland** (experimental): a Buildroot root filesystem for the
  hub's spare firmware slot that replaces all Xiaomi services. It boots in
  about 20 seconds, uses ~20 MB of RAM, runs the same services as above
  and falls back to the stock system by itself if a boot fails. See
  [OpenHub2 userland](#openhub2-userland).
- **Hardware documentation** in [doc/hardware.md](doc/hardware.md): SoC
  (SigmaStar Pioneer3 SSC020A), NAND layout, A/B slots, boot flow, Zigbee
  stack.

## How it works

The stock firmware runs `/data/scripts/post_init.sh` at boot when it exists,
instead of its own service launcher. OpenHub2 installs its hook there:

1. starts `gatessh`;
2. in `openhub2` mode, bind-mounts patched copies of `fw_manager.sh` and
   `app_monitor.sh` that leave out the stock Zigbee host and the stock BLE
   daemon (`miio_bt`). The read-only
   originals are never modified, and the patch is only applied if every
   check passes;
3. starts the stock services (`fw_manager.sh -r`), exactly as the firmware
   would;
4. in `openhub2` mode, starts `zbbridge` and `btproxy`;
5. starts `ohweb` (the web panel on port 80).

In this mode nothing is written to the system partitions: removing
`/data/scripts/post_init.sh` and rebooting restores the stock behaviour.

## OpenHub2 userland

An open root filesystem for the hub's spare rootfs slot (A), built with
Buildroot 2026.02 from [`userland/`](userland) by GitHub Actions (artifact
`openhub2-rootfs`, about 2.6 MB of the 16 MB slot). It keeps the stock
4.9.84 kernel and its Wi-Fi modules; the proprietary modules are not
shipped but copied from your own hub.

Boot: feed the hardware watchdog, mount `/data`, load the Wi-Fi modules,
join Wi-Fi with `wpa_supplicant` using the factory MAC, NTP, then the same
OpenHub2 binaries from `/data` (`gatessh`, `zbbridge`, `btproxy`, `ohweb`).

**Safety: "try once" boot.** The U-Boot environment gets a small chain of
variables. Every boot of OpenHub2 first saves the stock system as the next
choice; only a healthy boot (Wi-Fi address and SSH up) re-arms OpenHub2.
A failed or hung boot (the watchdog reboots, the kernel uses `panic=5`)
therefore comes back as stock without any intervention.

| LED | Meaning |
|---|---|
| purple blinking | OpenHub2 booting |
| green (or the color chosen in the panel) | OpenHub2 up and healthy |
| red | boot not healthy after 3 minutes, next boot is stock |
| orange blinking | switching to stock |

**Back to stock:** press the hub's button **three times** quickly, use
*Switch to stock* in the panel, or run `userland.sh disable`.

Install, from the stock system with OpenHub2 already set up:

```sh
# copy the rootfs.squashfs artifact to /data/openhub2/ first
sh /data/openhub2/userland.sh prepare        # stock modules, Wi-Fi config, factory MAC
sh /data/openhub2/userland.sh flash /data/openhub2/rootfs.squashfs
sh /data/openhub2/userland.sh enable         # install the try-once boot chain
reboot
```

`userland.sh remove` restores the original U-Boot variables. Slot A then
holds OpenHub2 instead of the older stock build that was there.

## Install

### Quick start (no Xiaomi app)

The hub can be configured without the Mi Home app by exploiting the setup
access point, where the miio token is revealed in cleartext.

1. **Network reset**: hold the hub button ~10 s until the LED blinks orange,
   then wait for it to blink blue (setup mode).
2. **Connect** your PC to the hub's open Wi-Fi (`lumi-gateway-mgl001_mibt…`).
3. **Build** (Go 1.26+): `make && make ohinstall`.
4. **Run** the installer:
   ```
   build/ohinstall -ssid "YourNetwork" -password "YourPassword"
   ```
   The installer discovers the hub, reads the token, **recovers the gateway
   key over LAN** (via `async_device_feedback_log`), opens a root shell,
   **deploys all files via SSH**, asks for a password and configures Wi-Fi.
   No Xiaomi app or cloud account needed.

   If automatic key recovery fails on your firmware, pass it manually with
   `-key <16-char-key>` (get it from `cat /data/miio/device.conf` over SSH,
   or via [XiaomiGateway3](https://github.com/AlexxIT/XiaomiGateway3)).

   Use `-no-deploy` to skip SSH deployment and do it manually.

5. In Home Assistant: **Add integration → ZHA**, radio type **EZSP**, path
   `socket://<hub-ip>:8888`.
6. Open `http://<hub-ip>/` in a browser for the web panel (same password
   as SSH).

### Manual install

If you already have root access (telnet or SSH):

1. `make` → produces `build/gatessh`, `build/zbbridge`, `build/ohweb`,
   `build/btproxy`.
2. Copy to the hub:
   - `build/gatessh` → `/data/gatessh/gatessh`
   - `build/zbbridge` → `/data/openhub2/zbbridge`
   - `build/ohweb` → `/data/openhub2/ohweb`
   - `build/btproxy` → `/data/openhub2/btproxy`
   - `hub/data/openhub2/*` → `/data/openhub2/`
   - `hub/data/scripts/post_init.sh` → `/data/scripts/post_init.sh`
   - `chmod +x` them all.
3. Set the SSH password: `/data/gatessh/gatessh -set-password`.
4. Switch mode: `/data/openhub2/ohctl mode openhub2` (reboots).

## Caveats

- **Pairing through ZHA can need a second try.** In testing, the first ZHA
  pairing attempts got no join request at the radio, while the same button
  joined immediately through plain zigpy/bellows on the same network. Once
  it had joined, ZHA picked it up on the next reset. Root cause still
  unknown: if a device does not show up, reset it again with the pairing
  window open.

- **Zigbee and Bluetooth devices do not migrate.** In `openhub2` mode the Mi
  Home Zigbee devices stop working and must be paired again in ZHA, and the
  hub no longer acts as Mi Home's Bluetooth/Mesh gateway: BLE sensors are
  read through Home Assistant instead (encrypted Xiaomi sensors need their
  bindkey).
- The radio runs EmberZNet **6.6.6** (EZSP v7). ZHA works with it.
  Zigbee2MQTT's current Ember driver expects newer firmware, so that is
  untested.
- Back up your hub's network settings before experimenting, for example with
  `zigpy radio ezsp socket://<hub-ip>:8888 backup file.json`.
- Accepting a firmware update from Mi Home may remove the boot hook.
- Do not publish NAND dumps: they contain per-device secrets (Wi-Fi password,
  cloud token, keys).

## Roadmap

- ~~One-step installer~~: `ohinstall` recovers the gateway key, opens root
  shell, deploys files over SSH, sets the password and configures Wi-Fi
  from the setup AP — fully automated.
- ~~Bluetooth proxy for Home Assistant~~: `btproxy` (scanning). Next: active
  connections and API encryption.
- First-run setup wizard in the panel, and a setup access point in the
  userland when no Wi-Fi is configured.
- Hub button, LED and resource sensors as ESPHome entities in Home
  Assistant.
- Updates (userland image and binaries) from the panel.
- NCP firmware upgrade (newer EmberZNet, for Zigbee2MQTT).
- ~~Full open userland (Buildroot) on the hub's spare A/B slot~~: see
  [OpenHub2 userland](#openhub2-userland).

## Credits

OpenHub2 builds on the work of many projects and people.

**Knowledge and reference implementations**

- [AlexxIT/XiaomiGateway3](https://github.com/AlexxIT/XiaomiGateway3) (MIT):
  the miio telnet method, the gateway key retrieval and a lot of knowledge
  about these hubs. The facts about the Zigbee reset GPIO and UART come
  from public community work around AlexxIT's `openmiio_agent`.
- [zigpy](https://github.com/zigpy) / [bellows](https://github.com/zigpy/bellows)
  (GPL-3.0): the ASH randomization and EZSP framing were checked against
  bellows, and zigpy/bellows were used to test the coordinator.
- [ESPHome](https://github.com/esphome/esphome): the native API messages
  implemented by `btproxy` follow `api.proto`; the proxy feature flags
  follow [aioesphomeapi](https://github.com/esphome/aioesphomeapi).
- [Zephyr](https://github.com/zephyrproject-rtos/zephyr): the stock BLE
  daemon embeds Zephyr's Bluetooth host and H5 driver, which documented
  how the hub's Telink controller is driven.
- Silicon Labs UG101 (*UART-EZSP Gateway Protocol Reference*) and the
  Bluetooth Core Specification (Three-wire UART transport, HCI,
  advertising data).

**Tools used to study the hardware and firmware**

- [Ghidra](https://github.com/NationalSecurityAgency/ghidra) (NSA,
  Apache-2.0), run on [Eclipse Temurin](https://adoptium.net/) JDK:
  decompilation of the stock BLE daemon and of U-Boot.
- [REA](https://github.com/morluto/rea) (`rea-agents`): reverse-engineering
  workflow and tooling.
- [PySquashfsImage](https://github.com/matteomattei/PySquashfsImage):
  offline inspection of squashfs images.

**Built with**

- [Go](https://go.dev/) and `golang.org/x/{crypto,net,sys}` (BSD-3-Clause),
  [creack/pty](https://github.com/creack/pty) (MIT),
  [pkg/sftp](https://github.com/pkg/sftp) (BSD-2-Clause).
- The userland image: [Buildroot](https://buildroot.org/) (GPL-2.0),
  [BusyBox](https://busybox.net/) (GPL-2.0), [musl](https://musl.libc.org/)
  (MIT), [wpa_supplicant](https://w1.fi/wpa_supplicant/) (BSD),
  [iw](https://wireless.wiki.kernel.org/en/users/documentation/iw) (ISC),
  [mtd-utils](http://www.linux-mtd.infradead.org/) (GPL-2.0),
  [U-Boot](https://u-boot.org/) tools `fw_printenv`/`fw_setenv` (GPL-2.0).
  Their sources are fetched and built by Buildroot; the image keeps their
  licenses.
- [GitHub Actions](https://github.com/features/actions) builds the image.

**Not included:** the Xiaomi/SigmaStar kernel, kernel modules and firmware
are not redistributed. The userland uses the copies already on your hub.

## License

MIT, see [LICENSE](LICENSE).
