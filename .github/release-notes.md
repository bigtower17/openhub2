Open tooling for the **Xiaomi Smart Home Hub 2** (`ZNDMWG04LM` / `BHR6765GL`, `lumi.gateway.mgl001`), tested on stock firmware `1.0.7_0019`.

> **Early release, tested on one unit.** Back up first. Not affiliated with Xiaomi.

### What it does

- **Zigbee coordinator for Home Assistant ZHA**, any brand: `socket://<hub-ip>:8888`, radio type EZSP.
- **ESPHome Bluetooth proxy** on the hub's BLE radio, discovered by Home Assistant automatically.
- **Web panel** on port 80: live dashboard, 24 h charts, Zigbee and Bluetooth devices, Wi-Fi, logs.
- **Root SSH** (`gatessh`) with SFTP.
- **OpenHub2 userland** (experimental): an open system for the spare firmware slot, about 20 s boot and ~20 MB RAM, falling back to stock by itself if a boot fails. Three presses of the hub's button go back to stock.

### Files

| File | What it is |
|---|---|
| `ohinstall-*` | installer for your PC: sets the hub up from its setup access point, without the Xiaomi app |
| `openhub2-hub-armv7-*.tar.gz` | everything that goes on the hub, laid out as on the hub (`/data/...`) |
| `openhub2-userland-*.squashfs` | the userland root filesystem for the spare slot |
| `openhub2-userland-legal-info-*.tar.gz` | licenses and sources of the open-source components in the userland image |
| `SHA256SUMS` | checksums |

### Install

See the [README](https://github.com/bigtower17/openhub2#install). In short: run `ohinstall` (or copy the hub archive by hand), switch to `openhub2` mode, then point ZHA at the hub. The userland is optional and installed with `userland.sh` from the stock system (see the README).

No Xiaomi or SigmaStar binaries are included: the userland uses the kernel modules already on your hub.
