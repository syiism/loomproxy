package web

import (
	"embed"
	"io/fs"
	"log"
)

// Embed the built `dist` directory (include nested asset files)
//
//go:embed dist/* dist/assets/*
var embeddedFS embed.FS

// FS is the filesystem rooted at the contents of dist/ so the server can
// serve files directly at /panel/ (index.html will be at FS's root).
var FS fs.FS

func init() {
	var err error
	FS, err = fs.Sub(embeddedFS, "dist")
	if err != nil {
		log.Fatalf("failed to prepare embedded web FS: %v", err)
	}
}
