#!/usr/bin/env bash
# Package: one file carrying the daemon, the CLI and the whole QML tree.
#
#   make release                        # → dist/dcode-linux-<arch>/
#   ARCH=arm64 make release             # cross-compile; pure Go, no cgo, so this just works
#   VERSION=1.2.3 scripts/build-release.sh
#
# Same shape as upstream dcal: one static binary + assets/ + scripts/, so untarring and running
# install-release.sh installs to /usr/local (or PREFIX=/usr).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# The version format lives in core/Makefile; ask it instead of writing a second copy here.
VERSION="${VERSION:-$("${MAKE:-make}" -s -C "$ROOT/core" version)}"
COMMIT="${COMMIT:-$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)}"
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
# The directory and archive carry the architecture, so amd64 and arm64 never overwrite each other.
# An explicit ARCH wins; otherwise take what `go env` reports, which also honours GOARCH=arm64.
ARCH="${ARCH:-$(go env GOARCH 2>/dev/null || echo amd64)}"
NAME="${NAME:-dcode-linux-$ARCH}"
BIN="dcode"                # the inner binary is dcode, matching the installed name and PATH
OUT="${OUT:-$ROOT/dist}"

# --- upstream contract (warn only by default: a half-cloned machine should still be able to build) ---
if ! contract="$("$ROOT/tools/check-upstream.sh" 2>&1)"; then
  echo "$contract"
  echo "(upstream contract check failed; set STRICT=1 to make it block the build)"
  [ "${STRICT:-0}" = "1" ] && exit 1
fi

# --- build ---
# Build details live in one place, core/Makefile (including the tar -h that flattens the UI into the
# embed directory).
# This only passes the version and assembles the artifacts.
mkdir -p "$OUT/$NAME"
"${MAKE:-make}" -C "$ROOT/core" build VERSION="$VERSION" BUILD_TIME="$BUILT_AT" COMMIT="$COMMIT"
# Write alongside, then rename: an old release binary may still be running under some daemon, and a
# direct cp over it fails with "Text file busy". rename swaps the directory entry only, leaving running
# processes alone.
cp "$ROOT/core/bin/dcode" "$OUT/$NAME/$BIN.tmp"
mv -f "$OUT/$NAME/$BIN.tmp" "$OUT/$NAME/$BIN"

# --- desktop assets and install scripts ---
mkdir -p "$OUT/$NAME/assets" "$OUT/$NAME/scripts"
install -m 644 "$ROOT/assets/com.nekolovo.dcode.svg" "$OUT/$NAME/assets/"
install -m 644 "$ROOT/assets/com.nekolovo.dcode.desktop" "$OUT/$NAME/assets/"
install -m 755 "$ROOT/scripts/install-assets.sh" "$OUT/$NAME/scripts/"
install -m 755 "$ROOT/scripts/install-release.sh" "$OUT/$NAME/scripts/"

tar -C "$OUT" -czf "$OUT/$NAME.tar.gz" "$NAME"

echo
echo "Version: $VERSION ($COMMIT)"
ls -lh "$OUT/$NAME/$BIN" "$OUT/$NAME.tar.gz"
echo "Install: tar xzf $NAME.tar.gz && sudo ./$NAME/scripts/install-release.sh $NAME/$BIN"
