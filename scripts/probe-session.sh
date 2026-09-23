#!/usr/bin/env bash
# probe-session.sh - prepare a deterministic xrdp session for input testing.
#
# The session runs an X input-event logger in the background and then starts a
# single xterm. Anything the RDP client sends (pointer motion, button presses,
# key presses) is therefore written to /tmp/xi.log, which can be read from
# outside the session. This separates "the client encoded the event wrongly"
# from "the server/X server dropped it".
#
# Usage:
#   sudo scripts/probe-session.sh
#   cat /tmp/xi.log            # after driving input through a client
set -euo pipefail

TARGET_USER=${TARGET_USER:-rdptest}
SESMAN_BIN=${SESMAN_BIN:-/usr/sbin/xrdp-sesman}
XRDP_BIN=${XRDP_BIN:-/usr/sbin/xrdp}

if [ "$(id -u)" -ne 0 ]; then
  echo "error: run as root, e.g. sudo $0" >&2
  exit 1
fi

if ! id "$TARGET_USER" >/dev/null 2>&1; then
  echo "error: user $TARGET_USER does not exist" >&2
  exit 1
fi

HOME_DIR=$(getent passwd "$TARGET_USER" | cut -d: -f6)

cat > "$HOME_DIR/.xsession" <<'XSESSION'
#!/bin/sh
# Log every X input event so a headless RDP client can be verified externally.
# - ER/XI2 events go to /tmp/xi.log (pointer motion, buttons, keys).
if command -v xinput >/dev/null 2>&1; then
    stdbuf -oL xinput test-xi2 --root >/tmp/xi.log 2>&1 &
fi
exec xterm -class UXTerm -u8
XSESSION

chmod 755 "$HOME_DIR/.xsession"
chown "$TARGET_USER" "$HOME_DIR/.xsession"
rm -f /tmp/xi.log
: > /tmp/xi.log
chown "$TARGET_USER" /tmp/xi.log
chmod 644 /tmp/xi.log

# Drop any existing session so the new .xsession is picked up.
pkill -u "$TARGET_USER" 2>/dev/null || true
sleep 1

for b in "$XRDP_BIN" "$SESMAN_BIN"; do
  pkill -x "$(basename "$b")" 2>/dev/null || true
done
sleep 1
mkdir -p /var/run/xrdp
"$SESMAN_BIN"
"$XRDP_BIN"
sleep 1

echo "probe session configured for user $TARGET_USER (display will be allocated on connect)"
echo "input event log: /tmp/xi.log"
