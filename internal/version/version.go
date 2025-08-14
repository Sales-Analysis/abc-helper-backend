package version

// Значения переопределяются на сборке через -ldflags -X ...
var (
	Version = "dev"
	Commit  = "none"
	BuiltAt = "unknown" // RFC3339 желательно
)

type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"built_at"`
}

func Get() Info {
	return Info{Version: Version, Commit: Commit, BuiltAt: BuiltAt}
}
