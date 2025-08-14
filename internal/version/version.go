// Package version contains build-time metadata exposed via the /version endpoint.
package version

// Values are overridden at build time via -ldflags -X.
var (
	Version = "dev"
	Commit  = "none"
	BuiltAt = "unknown" // RFC3339 timestamp is recommended
)

// Info is a JSON-serializable struct with build metadata.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"built_at"`
}

// Get returns current build information as Info.
func Get() Info {
	return Info{Version: Version, Commit: Commit, BuiltAt: BuiltAt}
}
