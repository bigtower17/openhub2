# Xiaomi Smart Home Hub 2 — hardware and firmware notes

Model `ZNDMWG04LM` / `BHR6765GL` (miio model `lumi.gateway.mgl001`), stock
firmware `1.0.7_0019`. Everything here was observed on a real unit, read-only,
unless stated otherwise.

## SoC and system

| | |
|---|---|
| SoC | SigmaStar **Pioneer3** `SSC020A` (`compatible = "sstar,pioneer3"`), 2× ARMv7 |
| RAM | 114 MB visible to Linux (`LX_MEM=0x7FE0000`) |
| Storage | raw NAND, 128 MB |
| Kernel | Linux 4.9.84, uImage, LZMA |
| Userland | BusyBox 1.20.2, squashfs (xz) root, read-only |
| Wi-Fi | SigmaStar `SSW105AT`, out-of-tree module `ssw105at.ko` |
| Bluetooth | `hci_uart` |
| Zigbee | Silicon Labs EFR32MG NCP on `/dev/ttyS1` |
| Audio | SigmaStar MI `mi_ao` / `mi_ai` (proprietary `mhal`, `mi_sys`) |
| Other | Ethernet, `/dev/fb0`, LED |

## NAND layout

From `mtdparts` (`nand0`, offset `0x140000`):

| mtd | name | size | content |
|---|---|---|---|
| 0 | BOOT0 | 1664k | U-Boot |
| 1 | BOOT1 | 1664k | U-Boot copy (identical) |
| 2 | ENV | 256k | U-Boot env (redundant pair with ENV1) |
| 3 | ENV1 | 256k | U-Boot env |
| 4 | KEY_CUST | 128k | |
| 5 | KERNEL | 5m | uImage, slot A |
| 6 | KERNEL_BAK | 5m | uImage, slot B |
| 7 | rootfs | 16m | squashfs, slot A |
| 8 | rootfs_bak | 16m | squashfs, slot B |
| 9 | factory | 1m | per-device factory data |
| 10 | RES | 20m | UBI → `/res` (holds `/etc/passwd`) |
| 11 | UBI | rest | UBI → `/data` (writable, ~50 MB) |

On the unit examined, slot A holds an older build (2022-09-22) and slot B the
running `1.0.7_0019` build (2023-09-25).

## Boot

U-Boot environment is redundant (ENV/ENV1, 4 KiB data, CRC32 + flag byte; the
copy with the higher flag wins). Relevant variables:

```
bootcmd=nand read.e 0x22000000 KERNEL_BAK 0x500000; dcache on; bootlogo 0 0 0 0; bootm 0x22000000;
        nand read.e 0x22000000 KERNEL 0x500000; dcache on; bootm 0x22000000
bootargs=root=/dev/mtdblock8 rootfstype=squashfs ro init=/linuxrc ...
```

- The kernel is always loaded from **KERNEL_BAK** first, KERNEL only if that
  `bootm` fails.
- The root filesystem is chosen by `bootargs` (`mtdblock7` = slot A,
  `mtdblock8` = slot B).
- There is no automatic fallback if the root filesystem fails after the kernel
  started.

`/etc/init.d/rcS` ends with:

```sh
CUSTOM_POST_INIT=/data/scripts/post_init.sh
if [ -x ${CUSTOM_POST_INIT} ]; then
    ${CUSTOM_POST_INIT} &
else
    fw_manager.sh -r
fi
```

`/data/scripts/post_init.sh` **replaces** `fw_manager.sh -r`, which starts all
stock services. A custom hook must call `/bin/fw_manager.sh -r` itself.

## Zigbee stack

```
EFR32MG NCP ─ /dev/ttyS1 ─▶ mZ3GatewayHost_MQTT ─ MQTT 127.0.0.1:1883 ─▶ mzigbee_agent ─▶ miio_client ─▶ cloud
             (EZSP/ASH)      (Silicon Labs Z3GatewayHost)                 (vendor filter)
```

- NCP firmware: **EmberZNet 6.6.6 GA build 218**, EZSP protocol **7**, stack
  type 2.
- Zigbee OTA image of the NCP kept at `/data/Network-Co-Processor.ota`
  (manufacturer `0x126E`, image type `0x1492`, file version 4).
- The "Xiaomi devices only" restriction lives in `mzigbee_agent` and the
  cloud, not in the radio or `Z3GatewayHost`.

## Access

Telnet can be opened over the LAN with the miio `system_command` method,
signed with the device token and the gateway key (the method implemented by
[XiaomiGateway3](https://github.com/AlexxIT/XiaomiGateway3)). Root has an
empty password after `passwd -d`. Telnet does not survive a reboot.

## Resets and setup mode

Two very different resets exist:

- **Network reset**, `/bin/remove_net.sh button|app`, triggered by
  `mha_master` from the button or the app. It stops the cloud services,
  clears only the cloud binding (`persist.app.miio_dtoken`, `miio_uid`,
  `miio_country`, `cloud_provisioned`, `/data/miio/config.db`, BLE pairing data,
  Matter certificates) and starts the setup access point. On `mgl001` the
  Wi-Fi credentials are kept. **Everything else in `/data` survives**,
  including a custom `post_init.sh`.
- **Factory reset**, `fw_manager.sh` → `factory_clean`, **formats `/data`**
  (`ubifs_format.sh 1 /data`).

`/bin/reset_wifi_info.sh <ssid> <password>` stores new Wi-Fi credentials and
reconnects in station mode, with no cloud involved.

### Setup access point

After a network reset, the hub starts an **open** access point on `p2p0`:

| | |
|---|---|
| SSID | `lumi-gateway-mgl001_mibt` + last 4 hex digits of the MAC |
| Hub address | `192.168.49.1` |
| Channel | 11 (observed) |

While unprovisioned, the hub answers the miio handshake (UDP 54321) with
its **device token in clear** in the checksum field, exactly like older miio
devices. Once provisioned, the same field is `ff…ff`. The revealed token was
verified with an encrypted `miIO.info` call.

This means the hub can be set up without the Mi Home app or a Xiaomi
account: join the setup AP, read the token from the handshake, then
configure it over miio.

