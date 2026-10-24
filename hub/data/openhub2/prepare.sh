#!/bin/sh
# Hide the stock Zigbee stack (mZ3GatewayHost_MQTT + mzigbee_agent) and the
# stock BLE daemon (miio_bt) so the EFR32 radio is free for zbbridge and the
# Telink BLE controller for btproxy. The read-only originals are never touched:
# patched copies are bind-mounted over them, and only if every check passes.
set -e
rm -f /tmp/openhub2-ready
W=/tmp/openhub2
mkdir -p $W

# fw_manager.sh: drop the lines that launch the Zigbee host, agent and miio_bt.
sed -e '/^[[:space:]]*mZ3GatewayHost_MQTT -p/d' \
    -e '/^[[:space:]]*mzigbee_agent -f/d' \
    -e '/^[[:space:]]*miio_bt -l3/d' /bin/fw_manager.sh > $W/fw_manager.sh
# app_monitor.sh: stop watching (and restarting) them.
sed -e '/^mzigbee_agent[[:space:]]/d' \
    -e '/^mZ3GatewayHost_MQTT[[:space:]]/d' \
    -e '/^miio_bt[[:space:]]/d' /bin/app_monitor.sh > $W/app_monitor.sh

# checks: valid shell, no launcher left, and only a few lines removed
# (1.0.7_0019 has 4 launch lines in fw_manager.sh, 3 map entries in app_monitor.sh)
sh -n $W/fw_manager.sh
sh -n $W/app_monitor.sh
! grep -q -E '^[[:space:]]*(mZ3GatewayHost_MQTT -p|mzigbee_agent -f|miio_bt -l3)' $W/fw_manager.sh
removed_fw=$(( $(wc -l < /bin/fw_manager.sh) - $(wc -l < $W/fw_manager.sh) ))
removed_mon=$(( $(wc -l < /bin/app_monitor.sh) - $(wc -l < $W/app_monitor.sh) ))
[ $removed_fw -ge 3 ] && [ $removed_fw -le 5 ]
[ $removed_mon -eq 3 ]

chmod 755 $W/fw_manager.sh $W/app_monitor.sh
mount -o bind $W/fw_manager.sh /bin/fw_manager.sh
mount -o bind $W/app_monitor.sh /bin/app_monitor.sh
touch /tmp/openhub2-ready
echo "stock Zigbee stack and miio_bt disabled"
