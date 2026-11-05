//go:build embedui

package uibundle

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

func init() {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return
	}
	FS = sub
}
