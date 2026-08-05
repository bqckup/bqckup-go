package storage

import (
	"fmt"
	"path"
	"strings"
)

// ValidateKey rejects object keys that are absolute, ambiguous, or escaping.
func ValidateKey(key string) error {
	if key == "" || strings.Contains(key, `\`) || path.IsAbs(key) || path.Clean(key) != key {
		return fmt.Errorf("unsafe storage key %q", key)
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("unsafe storage key %q", key)
		}
	}
	return nil
}

// JoinPrefix safely prepends an optional storage prefix to an object key.
func JoinPrefix(prefix, key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	if prefix == "" {
		return key, nil
	}
	if err := ValidateKey(prefix); err != nil {
		return "", err
	}
	return path.Join(prefix, key), nil
}
