#!/bin/sh
# $1 = target directory
set -e
T=$1
mkdir -p "$T/data" "$T/res" "$T/lib/modules"
# Stock kernel modules and Wi-Fi config are proprietary: the installer copies
# them from the user's own hub to /data/openhub2/stock.
ln -sfn /data/openhub2/stock/modules "$T/lib/modules/4.9.84"
ln -sfn /data/openhub2/stock/ssw105at-wifi.cfg "$T/etc/ssw105at-wifi.cfg"
cat > "$T/etc/os-release" <<EOF
NAME=OpenHub2
ID=openhub2
VERSION_ID=${OPENHUB2_VERSION:-dev}
PRETTY_NAME="OpenHub2 ${OPENHUB2_VERSION:-dev}"
EOF
chmod 755 "$T"/etc/init.d/S*
