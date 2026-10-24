#!/bin/sh
# OpenHub2 boot hook, run by /etc/init.d/rcS INSTEAD of "fw_manager.sh -r".
# Rule: whatever happens, the stock services must be started below, so every
# OpenHub2 step is optional and never fatal.

OH=/data/openhub2
MODE=$(cat $OH/mode 2>/dev/null)

# keep NAME CMD...: run CMD forever, restarting 5s after it exits. The binary
# is checked on every round, so one installed after boot is picked up too.
keep() {
    name=$1; shift
    ( while true; do
        [ -x "$1" ] && "$@" >>/tmp/$name.log 2>&1
        sleep 5
    done ) &
}

# 1. SSH first, so the hub stays reachable even if anything else misbehaves.
keep gatessh /data/gatessh/gatessh -addr :22

# 2. OpenHub2 mode: keep the stock Zigbee stack from starting.
if [ "$MODE" = "openhub2" ] && [ -x $OH/prepare.sh ]; then
    $OH/prepare.sh >/tmp/openhub2-prepare.log 2>&1 || true
fi

# 3. The stock Xiaomi services, exactly what rcS would have run.
/bin/fw_manager.sh -r

# 4. OpenHub2 mode: network Zigbee coordinator on TCP :8888.
if [ "$MODE" = "openhub2" ] && [ -f /tmp/openhub2-ready ]; then
    keep zbbridge $OH/zbbridge
fi

# 4b. Web panel on port 80 (any mode).
keep ohweb $OH/ohweb

# 5. Wi-Fi fallback: without a Xiaomi cloud binding the hub may come up in
#    setup mode (open AP) instead of joining the saved network. If there is
#    still no IP after 2 minutes, rejoin the saved Wi-Fi (credentials kept in
#    persist.app.wifi_*).
( sleep 120
  if ifconfig wlan0 2>/dev/null | grep -q "inet addr" || ifconfig eth0 2>/dev/null | grep -q "inet addr"; then
      echo "$(date) network ok at boot" >> $OH/wifi-fallback.log
  else
      echo "$(date) no IP after boot, rejoining saved Wi-Fi" >> $OH/wifi-fallback.log
      /bin/wifi_start.sh INIT >/dev/null 2>&1
      /bin/wifi_start.sh STA >/dev/null 2>&1
      sleep 30
  fi
  # 6. Clock: without the Xiaomi cloud nothing sets the time; keep it in
  #    sync with NTP (BusyBox ntpd daemonizes).
  if ! pidof ntpd >/dev/null; then
      ntpd -p pool.ntp.org -p time.google.com -p time.cloudflare.com
  fi ) &
