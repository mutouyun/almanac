//go:build !embedfrontend

package main

import "embed"

// staticFS is empty in the default build. The frontend is only embedded
// when building with the "embedfrontend" tag (see embed_dist.go), which CI
// does after copying web/dist into this directory. This keeps `go build`,
// `go test` and `go vet` working locally without a built frontend.
var staticFS embed.FS

// staticRoot is the sub-directory within staticFS to serve. "." means the
// (empty) root; the file server simply returns 404 for unknown paths while
// /health continues to work.
const staticRoot = "."
