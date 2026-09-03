package buildinfo

import "testing"

func TestCurrentUsesDevelopmentDefaults(t *testing.T) {
	info := Current()
<<<<<<< HEAD
	if info.Version != "v0.0.7" {
		t.Fatalf("version = %q, want v0.0.7", info.Version)
=======
	if info.Version != "v0.0.5" {
		t.Fatalf("version = %q, want v0.0.5", info.Version)
>>>>>>> 79c249c (feat: namespace backups and refresh v0.0.5 releases)
	}
	if info.Commit != "" {
		t.Fatalf("commit = %q, want empty", info.Commit)
	}
}
