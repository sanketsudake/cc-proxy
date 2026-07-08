// Package version exposes build metadata injected via -ldflags.
package version

var (
	Version = "dev"
	Commit  = "unknown"
)

func String() string { return Version + " (" + Commit + ")" }
