// Package buildinfo carries the version stamped into a released binary.
//
// It exists as its own package so code below main can read the version without
// main having to thread it through every call. main sets it from the `version`
// variable the release workflow injects with -X main.version=<tag>.
package buildinfo

// Version is the release tag, or "dev" in a build made without release ldflags.
var Version = "dev"
