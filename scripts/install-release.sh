#!/bin/sh
# Install from an unpacked release archive. The binary carries the UI, so one file is enough.
set -eu

if [ "$#" -ne 1 ] || [ ! -f "$1" ]; then
    echo "Usage: $0 /path/to/dcode" >&2
    exit 2
fi
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
prefix=${PREFIX:-/usr/local}
staging=${DESTDIR:-}

sh "$script_dir/install-assets.sh"
mkdir -p "$staging$prefix/bin"
install -m 755 "$1" "$staging$prefix/bin/dcode"
echo "Installed dcode and desktop assets under $staging$prefix"
