// Package web embeds the static site that phones open after scanning the QR code.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var static embed.FS

// Site returns the site's files rooted at index.html.
func Site() (fs.FS, error) {
	return fs.Sub(static, "static")
}
