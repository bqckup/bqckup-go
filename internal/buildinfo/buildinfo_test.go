package buildinfo

import "testing"

func TestCurrentUsesDevelopmentDefaults(t *testing.T) {
	info := Current()
	if info.Version != "v0.0.4" {
		t.Fatalf("version = %q, want v0.0.4", info.Version)
	}
	if info.Commit != "" {
		t.Fatalf("commit = %q, want empty", info.Commit)
	}
}
