package buildinfo

// Values are replaced by release builds through -ldflags.
var (
	version = "v0.0.7"
	commit  = ""
)

// Info identifies a built bqckup binary.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// Current returns build information for the running binary.
func Current() Info {
	return Info{Version: version, Commit: commit}
}
