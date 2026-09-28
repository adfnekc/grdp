#!/usr/bin/env bash
# gen-zgfx-vectors.sh - regenerate codec/testdata/zgfx_vectors.bin.
#
# ZGFX is the bulk compression the EGFX graphics channel uses. Its reference
# encoder in libfreerdp is a stub that only stores bytes uncompressed, so the
# streams have to come from our own test encoder. The reference half is
# FreeRDP's decoder: it decodes the streams and the result is stored next to
# them, so the Go tests can require byte for byte agreement.
#
# The streams are decoded in order through one decoder, as they would be on a
# real channel, because the decompression history is shared across messages.
#
# Requires libfreerdp3 and its WinPR runtime (no headers needed; the handful of
# declarations is repeated in the C file). On Debian/Ubuntu:
#
#   apt-get install libfreerdp3-3 libwinpr3-3
#
# Usage:
#   scripts/gen-zgfx-vectors.sh [output-file]
set -euo pipefail

OUT_FILE=${1:-codec/testdata/zgfx_vectors.bin}
SRC_DIR=$(cd "$(dirname "$0")/.." && pwd)
TMP_DIR=$(mktemp -d -t genzgfx.XXXXXX)
BIN=$(mktemp -t genzgfx.XXXXXX)

find_lib() {
  local name=$1
  for d in /usr/lib/x86_64-linux-gnu /usr/lib /usr/lib64 /usr/local/lib; do
    for p in "$d/$name.so.3" "$d/$name.so"; do
      [ -e "$p" ] && { echo "$p"; return 0; }
    done
  done
  return 1
}

cleanup() {
  rm -rf "$TMP_DIR" "$BIN"
}
trap cleanup EXIT

FREERDP_LIB=$(find_lib libfreerdp3) || { echo "error: libfreerdp3 not found" >&2; exit 1; }
WINPR_LIB=$(find_lib libwinpr3) || { echo "error: libwinpr3 not found" >&2; exit 1; }

# The Go side writes the streams to decode and the bytes they should produce.
echo "collecting streams..."
(cd "$SRC_DIR" && GRDP_ZGFX_VECTOR_DIR="$TMP_DIR" \
  go test ./codec -run TestZGFXWriteReferenceInputs -count=1 -v 2>&1 | grep -E "wrote|ok |FAIL" || true)

[ -s "$TMP_DIR/streams.bin" ] || { echo "error: no streams were written" >&2; exit 1; }

echo "decoding with libfreerdp..."
cc -O2 -Wall -o "$BIN" "$SRC_DIR/scripts/gen-zgfx-vectors.c" "$FREERDP_LIB" "$WINPR_LIB"
"$BIN" "$TMP_DIR" "$SRC_DIR/$OUT_FILE"

echo "vectors written to $OUT_FILE"
