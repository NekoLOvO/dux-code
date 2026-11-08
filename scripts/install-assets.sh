#!/bin/sh
# Desktop integration: the .desktop file and icons. Shared by source and binary installs.
set -eu

asset_dir=$(CDPATH= cd -- "$(dirname -- "$0")/../assets" && pwd)
desktop_id=com.nekolovo.dcode
prefix=${PREFIX:-/usr/local}
dcode_exec=${DCODE_EXEC:-$prefix/bin/dcode}
staging=${DESTDIR:-}
data_dir=${DATA_DIR:-$prefix/share}
applications_dir=${APPLICATIONS_DIR:-$data_dir/applications}
icon_dir=${ICON_DIR:-$data_dir/icons/hicolor/scalable/apps}

install_desktop() {
    if [ -z "$staging" ] && ! command -v update-desktop-database >/dev/null 2>&1; then
        echo 'Desktop registration requires desktop-file-utils (update-desktop-database).' >&2
        exit 1
    fi
    mkdir -p "$staging$applications_dir"
    sed "s|^Exec=dcode |Exec=$dcode_exec |" \
        "$asset_dir/$desktop_id.desktop" > "$staging$applications_dir/$desktop_id.desktop"
    chmod 644 "$staging$applications_dir/$desktop_id.desktop"
    if [ -z "$staging" ]; then
        update-desktop-database -q "$applications_dir"
    fi
}

install_icon() {
    mkdir -p "$staging$icon_dir"
    install -m 644 "$asset_dir/$desktop_id.svg" "$staging$icon_dir/$desktop_id.svg"
    if [ -z "$staging" ] && command -v gtk-update-icon-cache >/dev/null 2>&1; then
        gtk-update-icon-cache -q "$data_dir/icons/hicolor" 2>/dev/null || true
    fi
}

case "${1:-all}" in
    desktop) install_desktop ;;
    icon) install_icon ;;
    all)
        install_desktop
        install_icon
        ;;
    *) echo "Usage: $0 [all|desktop|icon]" >&2; exit 2 ;;
esac
