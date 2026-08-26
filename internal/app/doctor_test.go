package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenDoctorRecordsStorageConstructionFailures: a storage whose
// directory cannot be created must become a per-storage error, never a hard
// failure, so doctor reports the rest of the picture.
func TestOpenDoctorRecordsStorageConstructionFailures(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	roParent := filepath.Join(root, "ro-parent")
	require.NoError(t, os.MkdirAll(filepath.Join(configDir, "config"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(configDir, "sites"), 0o700))
	require.NoError(t, os.MkdirAll(roParent, 0o700))
	require.NoError(t, os.Chmod(roParent, 0o500))
	t.Cleanup(func() { _ = os.Chmod(roParent, 0o700) })

	require.NoError(t, os.WriteFile(filepath.Join(configDir, "bqckup.yaml"), []byte("version: 2\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config", "storages.yaml"), fmt.Appendf(nil, `storages:
  local-primary:
    type: local
    directory: %s
`, filepath.Join(roParent, "child")), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "sites", "example.yaml"), []byte(`version: 2
site:
  name: example
  enabled: true
  sources:
    files:
      include: [/tmp]
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 1h
    keep_last: 3
`), 0o600))

	checker, err := OpenDoctor(context.Background(), configDir)
	require.NoError(t, err)
	assert.Contains(t, checker.StoreErrs, "local-primary")
	assert.NotContains(t, checker.Stores, "local-primary")
}
