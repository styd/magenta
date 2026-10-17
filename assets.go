// Package magenta exposes the built-in rule and colorscheme files embedded in the binaries.
package magenta

import "embed"

// FS holds rules/*.toml and colorschemes/*.toml.
//
//go:embed rules/*.toml colorschemes/*.toml
var FS embed.FS
