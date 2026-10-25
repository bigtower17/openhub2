#!/bin/sh
# Install the OpenHub2 userland in the spare rootfs slot (A) and boot it with
# a U-Boot "try once" chain: each boot first saves the stock system as the
# next choice, and only a healthy OpenHub2 boot re-arms itself. A failed or
# hung boot (the watchdog reboots) therefore falls back to stock.
#
#   userland.sh prepare        copy stock modules/Wi-Fi config, write wpa conf
#   userland.sh flash FILE     write rootfs.squashfs to slot A (+ stock kernel)
#   userland.sh enable         install the try-once boot chain
#   userland.sh disable        next boot is stock (chain kept)
#   userland.sh remove         restore the stock boot variables
#   userland.sh status
set -e

OH=/data/openhub2
STOCK=$OH/stock
PRINTENV=/etc/fw_printenv
SETENV=/etc/fw_setenv
[ -x $PRINTENV ] || PRINTENV=fw_printenv
[ -x $SETENV ] || SETENV=fw_setenv

die() { echo "userland: $*" >&2; exit 1; }
getenv() { $PRINTENV "$1" 2>/dev/null | sed "s/^$1=//"; }

on_stock_b() { grep -q 'root=/dev/mtdblock8' /proc/cmdline; }

prepare() {
	on_stock_b || die "run prepare from the stock system (slot B)"
	mkdir -p $STOCK/modules
	for m in usb-common usbcore ehci-hcd ccm cfg80211 mac80211 ssw105at; do
		cp /lib/modules/4.9.84/$m.ko $STOCK/modules/
	done
	cp /etc/ssw105at-wifi.cfg $STOCK/
	# The Wi-Fi chip's own MAC differs: stock sets the factory one at boot.
	agetprop persist.sys.wlan_mac > $STOCK/wlan_mac
	ssid=$(agetprop persist.app.wifi_ssid)
	pass=$(agetprop persist.app.wifi_passphrase)
	[ -n "$ssid" ] || die "no Wi-Fi SSID stored (persist.app.wifi_ssid)"
	umask 077
	{
		echo "ctrl_interface=/var/run/wpa_supplicant"
		echo "network={"
		echo "	ssid=\"$ssid\""
		echo "	scan_ssid=1"
		if [ -n "$pass" ]; then
			echo "	psk=\"$pass\""
			echo "	key_mgmt=WPA-PSK"
		else
			echo "	key_mgmt=NONE"
		fi
		echo "}"
	} > $OH/wpa_supplicant.conf
	sync
	echo "prepared: $(ls $STOCK/modules | wc -l) modules, MAC $(cat $STOCK/wlan_mac), wpa_supplicant.conf for \"$ssid\""
}

write_mtd() { # write_mtd FILE MTDNUM: erase, write, read back and compare
	f=$1; m=$2
	size=$(wc -c < "$f")
	flash_erase /dev/mtd$m 0 0 >/dev/null
	nandwrite -p /dev/mtd$m "$f" >/dev/null
	want=$(md5sum < "$f" | cut -d' ' -f1)
	got=$(dd if=/dev/mtd$m bs=2048 count=$(( (size + 2047) / 2048 )) 2>/dev/null | head -c "$size" | md5sum | cut -d' ' -f1)
	[ "$want" = "$got" ] || die "verify failed on mtd$m"
}

flash() {
	f=$1
	on_stock_b || die "flash only from the stock system (slot B)"
	[ -f "$f" ] || die "usage: userland.sh flash rootfs.squashfs"
	[ "$(head -c 4 "$f")" = "hsqs" ] || die "$f is not a squashfs image"
	[ "$(wc -c < "$f")" -le 16777216 ] || die "$f is larger than the 16 MiB slot"
	[ -d $STOCK/modules ] || die "run 'userland.sh prepare' first"
	echo "writing rootfs to mtd7 (slot A)"
	write_mtd "$f" 7
	# The default U-Boot environment boots slot A: give it the running kernel.
	k=/tmp/kernel_b.img
	dd if=/dev/mtd6 of=$k bs=128k 2>/dev/null
	if ! dd if=/dev/mtd5 bs=128k 2>/dev/null | cmp -s - $k; then
		echo "copying the stock kernel to mtd5 (slot A)"
		write_mtd $k 5
	fi
	rm -f $k
	sync
	echo "flashed"
}

enable() {
	on_stock_b || die "enable only from the stock system (slot B)"
	boot=$(getenv bootcmd)
	case "$boot" in "run oh_next"*) boot=$(getenv boot_kernel) ;; esac
	args=$(getenv stock_args)
	[ -n "$args" ] || args=$(getenv bootargs)
	case "$args" in *mtdblock8*) ;; *) die "unexpected bootargs: $args" ;; esac
	[ -n "$boot" ] || die "no bootcmd"
	$SETENV stock_args "$args"
	# panic=5: a kernel panic reboots (and so falls back to stock) instead of
	# hanging before the watchdog is armed.
	$SETENV oh_args "$(echo "$args" | sed -e 's#root=/dev/mtdblock8#root=/dev/mtdblock7#' -e 's#init=/linuxrc#init=/sbin/init#') panic=5 oh=userland"
	$SETENV boot_kernel "$boot"
	$SETENV oh_stock 'setenv bootargs ${stock_args}; run boot_kernel'
	$SETENV oh_try 'setenv oh_next run oh_stock; saveenv; setenv bootargs ${oh_args}; run boot_kernel'
	$SETENV oh_next 'run oh_try'
	$SETENV bootcmd "run oh_next; $boot"
	rm -f $OH/userland.disabled
	sync
	echo "enabled: the next boot tries OpenHub2"
}

disable() {
	touch $OH/userland.disabled
	$SETENV oh_next 'run oh_stock'
	sync
	echo "disabled: the next boot is stock"
}

remove() {
	boot=$(getenv boot_kernel)
	[ -n "$boot" ] || die "boot chain not installed"
	$SETENV bootcmd "$boot"
	for v in oh_next oh_try oh_stock oh_args boot_kernel stock_args; do $SETENV $v; done
	touch $OH/userland.disabled
	sync
	echo "removed: stock boot variables restored"
}

status() {
	echo "running: $(grep -o 'root=/dev/mtdblock[0-9]' /proc/cmdline)$(grep -o ' oh=[a-z]*' /proc/cmdline)"
	echo "bootcmd: $(getenv bootcmd | cut -c1-40)"
	echo "oh_next: $(getenv oh_next)"
	[ -f $OH/userland.disabled ] && echo "userland: disabled" || true
	[ -f $OH/userland.log ] && tail -3 $OH/userland.log || true
}

case "$1" in
prepare) prepare ;;
flash) flash "$2" ;;
enable) enable ;;
disable) disable ;;
remove) remove ;;
status) status ;;
*) sed -n '7,13p' "$0"; exit 1 ;;
esac
