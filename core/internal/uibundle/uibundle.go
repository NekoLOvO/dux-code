// Package uibundle carries the UI (the QML tree Quickshell loads) inside the binary, so an
// install is a single file.
//
// The development build embeds nothing (FS is nil) and `dcode ui` looks next to the executable or
// in /usr/share/dcode/ui. The packaged build copies QML into dist/ via
// scripts/build-release.sh and compiles with -tags embedui, where embed.go fills FS.
package uibundle

import "io/fs"

// FS is the embedded UI tree; nil when nothing is embedded.
var FS fs.FS
