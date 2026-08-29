package buildinfo

import "testing"

func TestCurrentUsesDevelopmentDefaults(t *testing.T) {
	info := Current()
	if info.Version != "v0.0.4" {
		t.Fatalf("version = %q, want v0.0.4", info.Version)
	}
	if info.Commit != "local" {
		t.Fatalf("commit = %q, want local", info.Commit)
	}
}
