package buildinfo

// Values are replaced by release builds through -ldflags.
var (
<<<<<<< HEAD
	version = "v0.0.7"
	commit  = ""
=======
<<<<<<< HEAD
	version = "v0.0.4"
	commit  = "local"
=======
	version = "v0.0.5"
	commit  = ""
>>>>>>> 712cca7 (feat: namespace backups and refresh v0.0.5 releases)
>>>>>>> 79c249c (feat: namespace backups and refresh v0.0.5 releases)
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
