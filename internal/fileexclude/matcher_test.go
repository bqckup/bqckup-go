package fileexclude

import (
	"path/filepath"
	"testing"
)

func TestMatchAny(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "srv", "site")
	tests := []struct {
		name      string
		patterns  []string
		candidate string
		want      bool
	}{
		{"basename glob", []string{"*.tmp"}, filepath.Join(root, "deep", "skip.tmp"), true},
		{"recursive relative", []string{"cache/**"}, filepath.Join(root, "cache", "deep", "file"), true},
		{"recursive directory itself", []string{"cache/**"}, filepath.Join(root, "cache"), true},
		{"relative does not escape root", []string{"cache/**"}, filepath.Join(string(filepath.Separator), "cache", "file"), false},
		{"absolute subtree", []string{filepath.Join(root, "cache")}, filepath.Join(root, "cache", "file"), true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := MatchAny(test.patterns, test.candidate, []string{root}); got != test.want {
				t.Fatalf("MatchAny() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	if err := Validate("cache/**"); err != nil {
		t.Fatal(err)
	}
	if err := Validate("["); err == nil {
		t.Fatal("malformed pattern accepted")
	}
	if err := Validate("**/cache"); err == nil {
		t.Fatal("unsupported double-star placement accepted")
	}
}
