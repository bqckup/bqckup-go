package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDatabaseCommandsRequireSourceAndForce(t *testing.T) {
	root, _, _ := commandForTest(t, "database", "check", "eprints")
	check, _, err := root.Find([]string{"database", "check"})
	require.NoError(t, err)
	assert.NotNil(t, check.Flags().Lookup("source"))

	configDir, _ := writeCLIConfig(t)
	root, _, _ = commandForTest(t, "--config-dir", configDir, "database", "repair", "eprints", "--source", "repository", "--table", "legacy")
	err = root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--force")
}

func TestDatabaseRepairRequiresTable(t *testing.T) {
	root, _, _ := commandForTest(t, "database", "repair", "eprints", "--source", "repository", "--force")
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "table")
}
