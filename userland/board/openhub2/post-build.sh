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
# Go helpers built by the workflow (OPENHUB2_BIN_DIR).
if [ -n "$OPENHUB2_BIN_DIR" ]; then
	install -m 755 "$OPENHUB2_BIN_DIR/ohbutton" "$T/usr/sbin/ohbutton"
else
	echo "post-build: OPENHUB2_BIN_DIR not set, ohbutton not installed" >&2
fi
