package buildinfo

import "testing"

func TestCurrentUsesDevelopmentDefaults(t *testing.T) {
	info := Current()
	if info.Version != "v1.0.11" {
		t.Fatalf("version = %q, want v1.0.11", info.Version)
	}
	if info.Commit != "" {
		t.Fatalf("commit = %q, want empty", info.Commit)
	}
}

func TestUserAgent(t *testing.T) {
	if got := UserAgent(); got != "bqckup/1.0.3" {
		t.Fatalf("UserAgent() = %q, want %q", got, "bqckup/1.0.3")
	}
}

func TestFormatUserAgent(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "v1.0.3", want: "bqckup/1.0.3"},
		{input: "1.0.3", want: "bqckup/1.0.3"},
		{input: "v0.0.1-alpha", want: "bqckup/0.0.1-alpha"},
		{input: "dev", want: "bqckup/dev"},
		{input: "", want: "bqckup/dev"},
		{input: "   ", want: "bqckup/dev"},
		{input: "  v2.1.0  ", want: "bqckup/2.1.0"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := formatUserAgent(tt.input); got != tt.want {
				t.Errorf("formatUserAgent(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
