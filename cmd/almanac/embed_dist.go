//go:build embedfrontend

package main

import "embed"

// staticFS holds the built Astro frontend. CI copies web/dist into
// cmd/almanac/dist before building with:  go build -tags embedfrontend
//
//go:embed all:dist
var staticFS embed.FS

// staticRoot points at the copied dist directory inside staticFS.
const staticRoot = "dist"
