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
- **Web panel** (`ohweb`): a lightweight management page on port 80 with
  system status, mode switch, Wi-Fi configuration, log viewer and reboot.
  Protected by the same password as SSH.
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

Nothing is written to the system partitions. Removing `/data/scripts/post_init.sh`
and rebooting restores the stock behaviour.

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
- NCP firmware upgrade (newer EmberZNet, for Zigbee2MQTT).
- Full open userland (Buildroot) on the hub's spare A/B slot.

## Credits

- [AlexxIT/XiaomiGateway3](https://github.com/AlexxIT/XiaomiGateway3) (MIT):
  the miio telnet method, the gateway key retrieval and a lot of knowledge
  about these hubs.
- [zigpy](https://github.com/zigpy) / [bellows](https://github.com/zigpy/bellows):
  used to test the coordinator.
- The protocol facts about the hub's Zigbee reset GPIO and UART come from
  public community work around AlexxIT's `openmiio_agent`.

## License

MIT, see [LICENSE](LICENSE).
