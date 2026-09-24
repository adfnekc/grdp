#!/bin/sh
# xrdp-dev-boot - start the local xrdp test target at WSL startup.
#
# Installed as /usr/local/sbin/xrdp-dev-boot and referenced from
# /etc/wsl.conf [boot] command, because this WSL image has no systemd to
# enable a service in. It is idempotent, so it is safe to run when xrdp is
# already up.

XRDP_BIN=${XRDP_BIN:-/usr/sbin/xrdp}
SESMAN_BIN=${SESMAN_BIN:-/usr/sbin/xrdp-sesman}

mkdir -p /var/run/xrdp

pgrep -x "$(basename "$SESMAN_BIN")" >/dev/null 2>&1 || "$SESMAN_BIN"
pgrep -x "$(basename "$XRDP_BIN")" >/dev/null 2>&1 || "$XRDP_BIN"
