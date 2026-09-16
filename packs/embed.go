// Package packs exposes the packs orq-lite ships as an embedded filesystem, so
// a released binary can scaffold a runnable v2 project without network access.
//
// This is product, not sample material: `orq-lite init` installs from here.
// The runnable example project that consumes it lives in examples/governed-pack.
package packs

import "embed"

// FS contains every shipped pack, digest-verified, rooted at the pack name:
// development/pack is the current development pack and development/pack-v5 the
// frozen copy that keeps pinned refs resolving.
//
//go:embed development
var FS embed.FS
