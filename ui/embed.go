// Package ui embeds the built React UI (ui/dist, produced by `npm run
// build` - see Makefile's build-ui) into the Go binary, so a ko-built (or
// any CGO_ENABLED=0) binary is fully self-contained: no separate UI
// directory to copy into an image or mount at runtime.
//
// Embedding happens at `go build` time from whatever is on disk in dist/,
// not from a build step this package runs itself - build the UI first, or
// the binary embeds whatever was last built (or just .gitkeep, in a fresh
// checkout that never ran the UI build).
package ui

import "embed"

//go:embed all:dist
var DistFS embed.FS
