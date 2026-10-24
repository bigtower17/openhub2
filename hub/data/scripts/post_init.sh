#!/bin/sh
# OpenHub2 boot hook, run by /etc/init.d/rcS INSTEAD of "fw_manager.sh -r".
# Rule: whatever happens, the stock services must be started below, so every
# OpenHub2 step is optional and never fatal.

OH=/data/openhub2
MODE=$(cat $OH/mode 2>/dev/null)

# 1. SSH first, so the hub stays reachable even if anything else misbehaves.
if [ -x /data/gatessh/gatessh ]; then
    ( while true; do
        /data/gatessh/gatessh -addr :22 >>/tmp/gatessh.log 2>&1
        sleep 5
    done ) &
fi

# 2. OpenHub2 mode: keep the stock Zigbee stack from starting.
if [ "$MODE" = "openhub2" ] && [ -x $OH/prepare.sh ]; then
    $OH/prepare.sh >/tmp/openhub2-prepare.log 2>&1 || true
fi

# 3. The stock Xiaomi services, exactly what rcS would have run.
/bin/fw_manager.sh -r

# 4. OpenHub2 mode: network Zigbee coordinator on TCP :8888.
if [ "$MODE" = "openhub2" ] && [ -x $OH/zbbridge ] && [ -f /tmp/openhub2-ready ]; then
    ( while true; do
        $OH/zbbridge >>/tmp/zbbridge.log 2>&1
        sleep 5
    done ) &
fi
