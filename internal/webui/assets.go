package webui

import (
	"embed"
	"io/fs"
)

//go:embed dist/*
var assets embed.FS

func FS() fs.FS {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		return assets
	}
	return sub
}
