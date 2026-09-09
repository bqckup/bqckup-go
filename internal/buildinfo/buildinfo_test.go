package buildinfo

import "testing"

func TestCurrentUsesDevelopmentDefaults(t *testing.T) {
	info := Current()
	if info.Version != "v1.0.0" {
		t.Fatalf("version = %q, want v1.0.0", info.Version)
	}
	if info.Commit != "" {
		t.Fatalf("commit = %q, want empty", info.Commit)
	}
}
