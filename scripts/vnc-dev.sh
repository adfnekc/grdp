#!/usr/bin/env bash
#
# vnc-dev.sh - run a real VNC server for the RFB client, without root.
#
# WSL has no VNC server by default, and passwordless sudo here does not cover
# apt. This downloads the TigerVNC standalone-server and tools .deb files with
# `apt-get download` (which needs no root) and unpacks them under the work
# directory, then runs Xtigervnc straight from there. Xtigervnc is self
# contained: it is the X server and needs no X session, so a blank root window
# is enough to hand the client a framebuffer.
#
#   scripts/vnc-dev.sh start      start one server (see the environment below)
#   scripts/vnc-dev.sh stop       stop the server this script started
#   scripts/vnc-dev.sh status
#   scripts/vnc-dev.sh selftest   start a None server, run the live tests, stop;
#                                 then the same with VncAuth, and stop
#
# Environment:
#   VNC_DISPLAY       X display number, default :99
#   VNC_PORT          TCP port, default 5999
#   VNC_SECURITY      None (default) or VncAuth
#   VNC_PASSWORD      password for VncAuth, default workerpass (first 8 bytes)
#   VNC_GEOMETRY      default 1024x768
#   VNC_DEPTH         X server depth, default 24. Setting 16 gives a server whose
#                     default pixel format is 16 bpp, which is what makes the
#                     client's SetPixelFormat observable
#   GRDP_VNC_WORKDIR  default ${XDG_CACHE_HOME:-$HOME/.cache}/grdp-vnc-dev

set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="${GRDP_VNC_WORKDIR:-${XDG_CACHE_HOME:-$HOME/.cache}/grdp-vnc-dev}"
DISPLAY_NUM="${VNC_DISPLAY:-:99}"
PORT="${VNC_PORT:-5999}"
SECURITY="${VNC_SECURITY:-None}"
PASSWORD="${VNC_PASSWORD:-workerpass}"
GEOMETRY="${VNC_GEOMETRY:-1024x768}"
DEPTH="${VNC_DEPTH:-24}"
WIDTH="${GEOMETRY%x*}"
HEIGHT="${GEOMETRY#*x}"
PIDFILE="$WORKDIR/xvnc.pid"
LOGFILE="$WORKDIR/xvnc.log"
PASSFILE="$WORKDIR/vncpass"

log() { printf '%s\n' "vnc-dev: $*" >&2; }

# find_xvnc prints the path to a usable Xvnc/Xtigervnc, or nothing.
find_xvnc() {
    local c p
    for c in Xvnc Xtigervnc; do
        if p="$(command -v "$c" 2>/dev/null)"; then
            printf '%s\n' "$p"
            return 0
        fi
    done
    p="$WORKDIR/root/usr/bin/Xtigervnc"
    if [ -x "$p" ]; then
        printf '%s\n' "$p"
        return 0
    fi
    return 1
}

# unpack_debs <package>... downloads and unpacks packages into $WORKDIR/root.
unpack_debs() {
    command -v apt-get >/dev/null 2>&1 || {
        log "apt-get is not available to fetch: $*"
        return 1
    }
    command -v dpkg-deb >/dev/null 2>&1 || {
        log "dpkg-deb is not available to unpack the packages"
        return 1
    }
    log "downloading $* (no root needed)"
    mkdir -p "$WORKDIR/deb" "$WORKDIR/root"
    (cd "$WORKDIR/deb" && apt-get download "$@" >&2) || return 1
    local d
    for d in "$WORKDIR"/deb/*.deb; do
        dpkg-deb -x "$d" "$WORKDIR/root"
    done
}

ensure_xvnc() {
    if find_xvnc >/dev/null; then
        return 0
    fi
    log "no VNC server installed; fetching TigerVNC into $WORKDIR"
    unpack_debs tigervnc-standalone-server tigervnc-common || {
        log "could not obtain Xtigervnc; install a VNC server or allow apt"
        return 1
    }
    find_xvnc >/dev/null || {
        log "the unpacked packages contain no Xtigervnc"
        return 1
    }
}

# find_tigervncpasswd prints the path to tigervncpasswd, fetching tigervnc-tools
# if needed. It is not named tigervncpasswd so it cannot shadow the command.
find_tigervncpasswd() {
    local p
    if p="$(command -v tigervncpasswd 2>/dev/null)"; then
        printf '%s\n' "$p"
        return 0
    fi
    p="$WORKDIR/root/usr/bin/tigervncpasswd"
    if [ -x "$p" ]; then
        printf '%s\n' "$p"
        return 0
    fi
    unpack_debs tigervnc-tools || return 1
    [ -x "$p" ] && printf '%s\n' "$p"
}

port_open() { (exec 3<>"/dev/tcp/127.0.0.1/$PORT") >/dev/null 2>&1; }

is_running() {
    [ -f "$PIDFILE" ] || return 1
    local pid
    pid="$(cat "$PIDFILE" 2>/dev/null)" || return 1
    [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}

start() {
    ensure_xvnc || return 1
    local xvnc
    xvnc="$(find_xvnc)"

    if is_running; then
        log "a server started by this script is already running on port $PORT (pid $(cat "$PIDFILE"))"
        return 0
    fi
    if port_open; then
        log "port $PORT is already in use by another process; set VNC_PORT"
        return 1
    fi

    local args=(-desktop grdp -geometry "$GEOMETRY" -depth "$DEPTH" -AlwaysShared
                -localhost=0 -rfbport "$PORT")
    if [ "$SECURITY" = "VncAuth" ]; then
        local passwd_bin
        passwd_bin="$(find_tigervncpasswd)" || {
            log "VncAuth needs tigervncpasswd"
            return 1
        }
        mkdir -p "$WORKDIR"
        printf '%s' "$PASSWORD" | "$passwd_bin" -f >"$PASSFILE"
        args+=(-SecurityTypes VncAuth -PasswordFile "$PASSFILE")
    else
        args+=(-SecurityTypes None)
    fi

    mkdir -p "$WORKDIR"
    log "starting $xvnc $DISPLAY_NUM on 127.0.0.1:$PORT ($SECURITY, $GEOMETRY, depth $DEPTH)"
    "$xvnc" "$DISPLAY_NUM" "${args[@]}" >"$LOGFILE" 2>&1 &
    echo $! >"$PIDFILE"

    local i
    for i in $(seq 1 50); do
        if port_open; then
            log "listening on 127.0.0.1:$PORT (pid $(cat "$PIDFILE"))"
            return 0
        fi
        if ! kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
            log "the server exited; last log lines:"
            sed -n '1,20p' "$LOGFILE" >&2
            return 1
        fi
        sleep 0.2
    done
    log "the server did not start listening on $PORT (see $LOGFILE)"
    return 1
}

stop() {
    if is_running; then
        local pid
        pid="$(cat "$PIDFILE")"
        log "stopping pid $pid"
        kill "$pid" 2>/dev/null || true
        local i
        for i in $(seq 1 25); do
            kill -0 "$pid" 2>/dev/null || break
            sleep 0.2
        done
        kill -9 "$pid" 2>/dev/null || true
    fi
    rm -f "$PIDFILE"
}

status() {
    if is_running; then
        log "running on 127.0.0.1:$PORT (pid $(cat "$PIDFILE"), $SECURITY)"
    else
        log "not running"
        return 1
    fi
}

run_live() { # run_live <password>
    local password="$1"
    log "running the live VNC tests against 127.0.0.1:$PORT ($SECURITY)"
    if [ "$SECURITY" != "None" ] && ! command -v xclip >/dev/null 2>&1; then
        log "note: xclip is not installed, the clipboard live test will skip"
    fi
    (cd "$REPO" && \
        GRDP_VNC_ADDR="127.0.0.1:$PORT" \
        GRDP_VNC_PASSWORD="$password" \
        GRDP_VNC_XDISPLAY="$DISPLAY_NUM" \
        GRDP_VNC_WIDTH="$WIDTH" \
        GRDP_VNC_HEIGHT="$HEIGHT" \
        go test ./protocol/rfb/ -run TestLive -count=1 -v)
}

selftest() {
    local failed=0

    SECURITY=None
    start || failed=1
    if [ "$failed" = 0 ]; then run_live "" || failed=1; fi
    stop

    SECURITY=VncAuth
    start || failed=1
    if [ "$failed" = 0 ]; then run_live "$PASSWORD" || failed=1; fi
    stop

    # A 16 bpp server is what makes SetPixelFormat observable: its default
    # pixel format is 16 bpp, so the client only receives 32 bpp rectangles -
    # which the live test requires - if SetPixelFormat took effect.
    DEPTH=16
    SECURITY=None
    start || failed=1
    if [ "$failed" = 0 ]; then run_live "" || failed=1; fi
    stop

    if [ "$failed" != 0 ]; then
        log "selftest failed"
        return 1
    fi
    log "selftest passed"
}

cmd="${1:-}"
case "$cmd" in
    start)    start ;;
    stop)     stop ;;
    status)   status ;;
    selftest) selftest ;;
    *)
        printf 'usage: %s <start|stop|status|selftest>\n' "$0" >&2
        exit 2
        ;;
esac
