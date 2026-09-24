#!/usr/bin/env bash
# dev-rdp.sh - start/stop a local xrdp target for integration testing.
#
# WSL has no systemd (PID 1 is not systemd), so "service xrdp start" may not
# work. This script starts xrdp-sesman and xrdp directly via sudo.
#
# Usage:
#   scripts/dev-rdp.sh up       # start xrdp + sesman
#   scripts/dev-rdp.sh down     # stop them
#   scripts/dev-rdp.sh restart
#   scripts/dev-rdp.sh status
#   scripts/dev-rdp.sh probe    # check TCP 3389 + RDP negotiation bytes
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)

XRDP_BIN=${XRDP_BIN:-/usr/sbin/xrdp}
SESMAN_BIN=${SESMAN_BIN:-/usr/sbin/xrdp-sesman}
RUN_DIR=${RUN_DIR:-/var/run/xrdp}
PORT=${PORT:-3389}

# have_dev_sudo reports whether the one time setup has been run. It asks sudo
# to list what is permitted rather than running anything, because sudo itself
# is not the thing we were granted: individual commands are, so `sudo -n true`
# would fail even when everything needed is allowed.
have_dev_sudo() {
  sudo -n -l 2>/dev/null | grep -qF "$SESMAN_BIN"
}

need_root() {
  [ "$(id -u)" -eq 0 ] && return 0
  if have_dev_sudo; then
    return 0
  fi
  echo "error: the action '$1' needs root, and this shell has no passwordless access." >&2
  echo >&2
  echo "       Run the one time setup, and it will stop asking you:" >&2
  echo "         sudo $SCRIPT_DIR/dev-rdp-setup.sh" >&2
  echo >&2
  echo "       (or, for a single run:  sudo $0 $1)" >&2
  exit 1
}

as_root() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
  else
    sudo -n "$@"
  fi
}

is_running() {
  pgrep -x "$(basename "$1")" >/dev/null 2>&1
}

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
      as_root pkill -x "$(basename "$b")" || true
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

case "${1:-}" in
  up) up ;;
  down) down ;;
  restart) down; up ;;
  status) status ;;
  probe) shift; probe "${1:-}" ;;
  *) echo "usage: $0 {up|down|restart|status|probe [host]}" >&2; exit 2 ;;
esac
