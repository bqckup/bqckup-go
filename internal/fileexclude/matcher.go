// Package fileexclude implements the exclude semantics shared by full and
// incremental file backups.
package fileexclude

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Validate checks the supported filepath glob syntax. A double star is
// deliberately limited to a trailing "/**", whose meaning is recursive
// directory exclusion.
func Validate(pattern string) error {
	if pattern == "" {
		return nil
	}
	if _, err := filepath.Match(pattern, ""); err != nil {
		return err
	}
	if strings.Contains(pattern, "**") && !strings.HasSuffix(filepath.ToSlash(pattern), "/**") {
		return fmt.Errorf("** is only supported as a trailing /**")
	}
	return nil
}

// MatchAny reports whether candidate matches any pattern. Relative patterns
// are evaluated against the basename and paths relative to each include root.
// Absolute paths without glob metacharacters retain the legacy exact/subtree
// behavior used by full backups.
func MatchAny(patterns []string, candidate string, roots []string) bool {
	for _, pattern := range patterns {
		if match(pattern, candidate, roots) {
			return true
		}
	}
	return false
}

func match(pattern, candidate string, roots []string) bool {
	if pattern == "" {
		return false
	}
	candidate = filepath.Clean(candidate)
	pattern = filepath.Clean(pattern)

	if filepath.IsAbs(pattern) {
		if recursiveBase, ok := recursivePrefix(pattern); ok {
			return within(candidate, recursiveBase)
		}
		if !hasMeta(pattern) {
			return within(candidate, pattern)
		}
		matched, _ := filepath.Match(pattern, candidate)
		return matched
	}

	if matched, _ := filepath.Match(pattern, filepath.Base(candidate)); matched {
		return true
	}
	for _, root := range roots {
		relative, err := filepath.Rel(filepath.Clean(root), candidate)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		if recursiveBase, ok := recursivePrefix(pattern); ok {
			if within(relative, recursiveBase) {
				return true
			}
			continue
		}
		if matched, _ := filepath.Match(pattern, relative); matched {
			return true
		}
	}
	return false
}

func recursivePrefix(pattern string) (string, bool) {
	if !strings.HasSuffix(filepath.ToSlash(pattern), "/**") {
		return "", false
	}
	return filepath.Clean(pattern[:len(pattern)-3]), true
}

func within(candidate, base string) bool {
	candidate = filepath.Clean(candidate)
	base = filepath.Clean(base)
	return candidate == base || strings.HasPrefix(candidate, base+string(filepath.Separator))
}

func hasMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}
