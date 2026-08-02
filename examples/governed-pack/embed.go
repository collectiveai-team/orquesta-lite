// Package governedpack exposes the canonical development pack as an embedded
// filesystem so a released orq-lite binary can scaffold a runnable v2 project.
package governedpack

import "embed"

// FS contains the complete, digest-verified development pack.
//
//go:embed pack
var FS embed.FS
