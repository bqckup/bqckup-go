package process

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunConfiguredEnvOverridesInheritedEnv pins the env precedence
// contract: spec.Env is appended after os.Environ(), and os/exec dedupes
// duplicate keys in favor of the LAST value, so a configured database
// password must beat an inherited one (the opposite would run dumps with
// a credential the user never configured).
func TestRunConfiguredEnvOverridesInheritedEnv(t *testing.T) {
	t.Setenv("MYSQL_PWD", "parent-value")

	var stdout, stderr bytes.Buffer
	err := NewProcessRunner().Run(context.Background(), ProcessSpec{
		Command: "sh",
		Args:    []string{"-c", `printf '%s' "$MYSQL_PWD"`},
		Env:     []string{"MYSQL_PWD=configured-value"},
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	require.NoError(t, err)
	assert.Equal(t, "configured-value", stdout.String())
	assert.Empty(t, stderr.String())
}
