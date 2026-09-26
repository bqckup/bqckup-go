package buildinfo

import "strings"

// Values are replaced by release builds through -ldflags.
var (
	version = "v1.0.12"
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

// UserAgent returns the canonical HTTP User-Agent header value for bqckup.
func UserAgent() string {
	return formatUserAgent(version)
}

func formatUserAgent(ver string) string {
	v := strings.TrimSpace(ver)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		v = "dev"
	}
	return "bqckup/" + v
}
