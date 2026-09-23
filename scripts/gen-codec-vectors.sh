#!/usr/bin/env bash
# gen-codec-vectors.sh - regenerate the codec test vectors in codec/testdata.
#
# The vectors are produced by the NSCodec and RemoteFX encoders inside
# libfreerdp, together with the pixels from FreeRDP's own decoders. That lets
# the Go tests check our decoder against the reference implementation rather
# than only against the original image, which would conflate "my decoder is
# wrong" with "this codec is lossy".
#
# Requires libfreerdp3 and its WinPR runtime (no headers needed; the handful of
# declarations is repeated in the C file). On Debian/Ubuntu:
#
#   apt-get install libfreerdp3-3 libwinpr3-3
#
# Usage:
#   scripts/gen-codec-vectors.sh [output-dir]
set -euo pipefail

OUT_DIR=${1:-codec/testdata}
SRC_DIR=$(cd "$(dirname "$0")/.." && pwd)
BIN=$(mktemp -t genvec.XXXXXX)

find_lib() {
  local name=$1
  for d in /usr/lib/x86_64-linux-gnu /usr/lib /usr/lib64 /usr/local/lib; do
    for p in "$d/$name.so.3" "$d/$name.so"; do
      [ -e "$p" ] && { echo "$p"; return 0; }
    done
  done
  return 1
}

FREERDP_LIB=$(find_lib libfreerdp3) || { echo "error: libfreerdp3 not found" >&2; exit 1; }
WINPR_LIB=$(find_lib libwinpr3) || { echo "error: libwinpr3 not found" >&2; exit 1; }

mkdir -p "$OUT_DIR"

cc -O2 -Wall -o "$BIN" "$SRC_DIR/scripts/gen-codec-vectors.c" "$FREERDP_LIB" "$WINPR_LIB"
"$BIN" "$OUT_DIR"
rm -f "$BIN"

echo "vectors written to $OUT_DIR"
