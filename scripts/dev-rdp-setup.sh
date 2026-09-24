#!/usr/bin/env bash
# dev-rdp-setup.sh - one time setup so the local xrdp target no longer needs
# manual root intervention.
#
# Run this once with sudo:
#
#   sudo scripts/dev-rdp-setup.sh
#
# It does three things:
#
#   1. Installs a small root owned boot script and points /etc/wsl.conf at it,
#      so xrdp is already running whenever the WSL image starts. This image has
#      no systemd, so the xrdp.service unit cannot be used.
#   2. Installs a root owned copy of the session probe helper, so resetting the
#      test session does not need a password either.
#   3. Grants the development user passwordless sudo for exactly the commands
#      needed to start, stop and reset the target, and nothing else.
#
# After this, scripts/dev-rdp.sh works without a password. Restart WSL once
# (`wsl --shutdown` from Windows) for the boot hook to take effect.
set -euo pipefail

TARGET_USER=${TARGET_USER:-rdptest}
PORT=${PORT:-3389}

XRDP_BIN=${XRDP_BIN:-/usr/sbin/xrdp}
SESMAN_BIN=${SESMAN_BIN:-/usr/sbin/xrdp-sesman}
PKILL_BIN=${PKILL_BIN:-/usr/bin/pkill}

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)

if [ "$(id -u)" -ne 0 ]; then
  echo "error: run as root, e.g. sudo $0" >&2
  exit 1
fi

# The user who should get the passwordless rules: whoever invoked sudo.
DEV_USER=${DEV_USER:-${SUDO_USER:-}}
if [ -z "$DEV_USER" ] || [ "$DEV_USER" = root ]; then
  echo "error: cannot tell which user to grant access to; set DEV_USER=name" >&2
  exit 1
fi
if ! id "$DEV_USER" >/dev/null 2>&1; then
  echo "error: user $DEV_USER does not exist" >&2
  exit 1
fi

BOOT_SCRIPT=/usr/local/sbin/xrdp-dev-boot
PROBE_SCRIPT=/usr/local/sbin/xrdp-dev-probe

# --- 1. boot hook ---------------------------------------------------------
install -m 0755 -o root -g root "$SCRIPT_DIR/dev-rdp-boot.sh" "$BOOT_SCRIPT"

if [ ! -f /etc/wsl.conf ]; then
  : > /etc/wsl.conf
fi
if grep -q '^\[boot\]' /etc/wsl.conf; then
  echo "/etc/wsl.conf already has a [boot] section; add this line yourself if needed:"
  echo "  command = $BOOT_SCRIPT"
else
  {
    echo
    echo '[boot]'
    echo "command = $BOOT_SCRIPT"
  } >> /etc/wsl.conf
  echo "added a [boot] command to /etc/wsl.conf"
fi

# --- 2. session probe helper ---------------------------------------------
install -m 0755 -o root -g root "$SCRIPT_DIR/probe-session.sh" "$PROBE_SCRIPT"

# --- 3. passwordless control ---------------------------------------------
SUDOERS=/etc/sudoers.d/xrdp-dev
TMP=$(mktemp)
cat > "$TMP" <<EOF
# Managed by scripts/dev-rdp-setup.sh.
#
# Lets the development user drive the local xrdp test target: start and stop
# it, reset one specific test session, and prepare the probe session. Nothing
# here runs a user-writable file as root, and no wildcards are used.
$DEV_USER ALL=(root) NOPASSWD: $SESMAN_BIN
$DEV_USER ALL=(root) NOPASSWD: $XRDP_BIN
$DEV_USER ALL=(root) NOPASSWD: $PKILL_BIN -x xrdp
$DEV_USER ALL=(root) NOPASSWD: $PKILL_BIN -x xrdp-sesman
$DEV_USER ALL=(root) NOPASSWD: $PKILL_BIN -u $TARGET_USER
$DEV_USER ALL=(root) NOPASSWD: /usr/bin/mkdir -p /var/run/xrdp
$DEV_USER ALL=(root) NOPASSWD: $PROBE_SCRIPT
EOF

if ! visudo -c -f "$TMP" >/dev/null 2>&1; then
  echo "error: generated sudoers file does not validate:" >&2
  cat "$TMP" >&2
  rm -f "$TMP"
  exit 1
fi
install -m 0440 -o root -g root "$TMP" "$SUDOERS"
rm -f "$TMP"
echo "installed $SUDOERS for user $DEV_USER"

# --- report ---------------------------------------------------------------
echo
echo "done. checks:"
# Verify the rule actually works rather than assuming it does.
if sudo -n -u root "$SESMAN_BIN" --version >/dev/null 2>&1 ||
   sudo -n -u root true >/dev/null 2>&1; then
  echo "  passwordless sudo: ok"
else
  echo "  passwordless sudo: could not verify (try: sudo -n -u root $XRDP_BIN)"
fi
echo "  boot script:  $BOOT_SCRIPT"
echo "  probe helper: $PROBE_SCRIPT"
echo "  port checked: $PORT"
echo
echo "restart WSL once for the boot hook to take effect:"
echo "  (from Windows)  wsl --shutdown"
