#!/usr/bin/env bash
# dev-rdp.sh - manage the local xrdp target used for integration testing.
#
# One entry point for everything related to the test target, because the
# operations share most of their state and splitting them across scripts made
# it unclear which one to run.
#
# Usage:
#   scripts/dev-rdp.sh setup           # one time, with sudo: stop asking for a password
#   scripts/dev-rdp.sh up              # start xrdp + xrdp-sesman
#   scripts/dev-rdp.sh down            # stop them
#   scripts/dev-rdp.sh restart
#   scripts/dev-rdp.sh status
#   scripts/dev-rdp.sh probe [host]    # check that the port accepts connections
#   scripts/dev-rdp.sh probe-session   # deterministic session for input testing
#
# `probe-session` rewrites ~rdptest/.xsession so the session logs every X input
# event to /tmp/xi.log and then runs a single xterm. That is how keyboard and
# mouse handling is verified: compare what the client sent with what the X
# server actually received, instead of squinting at screenshots.
#
# This image has no systemd (PID 1 is not systemd), so "service xrdp start"
# does not work and the xrdp.service unit cannot be enabled. xrdp is therefore
# started on demand.
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)

XRDP_BIN=${XRDP_BIN:-/usr/sbin/xrdp}
SESMAN_BIN=${SESMAN_BIN:-/usr/sbin/xrdp-sesman}
PKILL_BIN=${PKILL_BIN:-/usr/bin/pkill}
RUN_DIR=${RUN_DIR:-/var/run/xrdp}
PORT=${PORT:-3389}
TARGET_USER=${TARGET_USER:-rdptest}

# The root owned copy that `setup` installs. Root operations are funnelled
# through it so no user-writable file is ever run as root.
INSTALLED=$(command -v xrdp-dev 2>/dev/null || true)
INSTALLED=${INSTALLED:-/usr/local/sbin/xrdp-dev}
SUDOERS=/etc/sudoers.d/xrdp-dev

# --- privilege helpers ----------------------------------------------------

# have_dev_sudo reports whether `setup` has been run. It asks sudo to list what
# is permitted rather than running something, because the grant is per command:
# `sudo -n true` fails even when everything needed is allowed.
have_dev_sudo() {
  sudo -n -l 2>/dev/null | grep -qF "$SESMAN_BIN"
}

need_root() {
  [ "$(id -u)" -eq 0 ] && return 0
  have_dev_sudo && return 0
  echo "error: '$1' needs root, and this shell has no passwordless access." >&2
  echo >&2
  echo "  Run the one time setup, and it will stop asking you:" >&2
  echo "    sudo $SCRIPT_DIR/dev-rdp.sh setup" >&2
  echo >&2
  echo "  Or, for a single run:  sudo $SCRIPT_DIR/dev-rdp.sh $1" >&2
  exit 1
}

as_root() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
  else
    sudo -n "$@"
  fi
}

is_running() { pgrep -x "$(basename "$1")" >/dev/null 2>&1; }

# --- target control -------------------------------------------------------

up() {
  need_root up
  as_root mkdir -p "$RUN_DIR"
  if ! is_running "$SESMAN_BIN"; then
    echo "starting xrdp-sesman..."
    as_root "$SESMAN_BIN"
  fi
  if ! is_running "$XRDP_BIN"; then
    echo "starting xrdp..."
    as_root "$XRDP_BIN"
  fi
  sleep 1
  status
}

down() {
  need_root down
  for b in "$XRDP_BIN" "$SESMAN_BIN"; do
    if is_running "$b"; then
      echo "stopping $(basename "$b")..."
      as_root "$PKILL_BIN" -x "$(basename "$b")" || true
    fi
  done
  status
}

status() {
  for b in "$XRDP_BIN" "$SESMAN_BIN"; do
    if is_running "$b"; then
      echo "$(basename "$b"): running"
    else
      echo "$(basename "$b"): stopped"
    fi
  done
  if (ss -ltn 2>/dev/null || netstat -ltn 2>/dev/null) | grep -q ":$PORT "; then
    echo "port $PORT: listening"
  else
    echo "port $PORT: not listening"
  fi
}

probe() {
  host=${1:-127.0.0.1}
  if ! command -v nc >/dev/null 2>&1; then
    echo "nc not installed; cannot probe" >&2
    exit 1
  fi
  if nc -z -w3 "$host" "$PORT"; then
    echo "RDP target $host:$PORT reachable"
  else
    echo "RDP target $host:$PORT NOT reachable" >&2
    exit 1
  fi
}

# --- deterministic session ------------------------------------------------

# probe_session_body must run as root; probe_session re-execs the installed
# root owned copy when it is not.
probe_session_body() {
  if ! id "$TARGET_USER" >/dev/null 2>&1; then
    echo "error: user $TARGET_USER does not exist" >&2
    exit 1
  fi

  HOME_DIR=$(getent passwd "$TARGET_USER" | cut -d: -f6)

  cat > "$HOME_DIR/.xsession" <<'XSESSION'
#!/bin/sh
# Log every X input event so a headless RDP client can be verified externally.
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
  "$PKILL_BIN" -u "$TARGET_USER" 2>/dev/null || true
  sleep 1
  for b in "$XRDP_BIN" "$SESMAN_BIN"; do
    "$PKILL_BIN" -x "$(basename "$b")" 2>/dev/null || true
  done
  sleep 1
  mkdir -p "$RUN_DIR"
  "$SESMAN_BIN"
  "$XRDP_BIN"
  sleep 1

  echo "probe session configured for $TARGET_USER; it is created on next connect"
  echo "X input events will be logged to /tmp/xi.log"
}

probe_session() {
  if [ "$(id -u)" -eq 0 ]; then
    probe_session_body
    return
  fi
  if [ -x "$INSTALLED" ]; then
    exec sudo -n "$INSTALLED" probe-session
  fi
  echo "error: 'probe-session' needs root and the helper is not installed." >&2
  echo "       run: sudo $SCRIPT_DIR/dev-rdp.sh setup" >&2
  exit 1
}

# --- one time setup -------------------------------------------------------

setup() {
  if [ "$(id -u)" -ne 0 ]; then
    echo "error: run as root:  sudo $SCRIPT_DIR/dev-rdp.sh setup" >&2
    exit 1
  fi

  DEV_USER=${DEV_USER:-${SUDO_USER:-}}
  if [ -z "$DEV_USER" ] || [ "$DEV_USER" = root ]; then
    echo "error: cannot tell which user to grant access to; set DEV_USER=name" >&2
    exit 1
  fi
  id "$DEV_USER" >/dev/null 2>&1 || { echo "error: no such user: $DEV_USER" >&2; exit 1; }

  # Install a root owned copy, so the passwordless grant below never runs a
  # file the development user could edit.
  install -m 0755 -o root -g root "$SCRIPT_DIR/dev-rdp.sh" "$INSTALLED"
  echo "installed $INSTALLED (root owned)"

  TMP=$(mktemp)
  cat > "$TMP" <<EOF
# Managed by dev-rdp.sh setup.
#
# Lets $DEV_USER drive the local xrdp test target: start and stop it, reset one
# specific test session, and prepare the probe session. The rules name
# individual binaries with fixed arguments, and the helper is a root owned
# copy, so this grants no ability to run arbitrary files as root.
$DEV_USER ALL=(root) NOPASSWD: $SESMAN_BIN
$DEV_USER ALL=(root) NOPASSWD: $XRDP_BIN
$DEV_USER ALL=(root) NOPASSWD: $PKILL_BIN -x xrdp
$DEV_USER ALL=(root) NOPASSWD: $PKILL_BIN -x xrdp-sesman
$DEV_USER ALL=(root) NOPASSWD: $PKILL_BIN -u $TARGET_USER
$DEV_USER ALL=(root) NOPASSWD: /usr/bin/mkdir -p $RUN_DIR
$DEV_USER ALL=(root) NOPASSWD: $INSTALLED probe-session
EOF

  if ! visudo -c -f "$TMP" >/dev/null 2>&1; then
    echo "error: generated sudoers file does not validate; nothing changed:" >&2
    cat "$TMP" >&2
    rm -f "$TMP"
    exit 1
  fi
  install -m 0440 -o root -g root "$TMP" "$SUDOERS"
  rm -f "$TMP"
  echo "installed $SUDOERS for $DEV_USER"
  echo
  echo "done. 'scripts/dev-rdp.sh up' no longer needs a password."
}

case "${1:-}" in
  setup) setup ;;
  up) up ;;
  down) down ;;
  restart) down; up ;;
  status) status ;;
  probe) shift; probe "${1:-}" ;;
  probe-session) probe_session ;;
  *)
    cat >&2 <<EOF
usage: $0 <command>

  setup          one time, with sudo: install helpers and stop asking for a password
  up             start xrdp-sesman and xrdp
  down           stop them
  restart
  status
  probe [host]   check that the RDP port accepts connections
  probe-session  deterministic session for input testing (needs setup)
EOF
    exit 2
    ;;
esac
