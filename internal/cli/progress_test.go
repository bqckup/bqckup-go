package cli

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteCheckStartText(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, writeCheckStartText(&out, "site-a", "s3-primary", false))
	assert.Equal(t, "[>] check:site-a: checking repository on s3-primary\n", out.String())

	out.Reset()
	require.NoError(t, writeCheckStartText(&out, "site-a", "s3-primary", true))
	assert.Equal(t, "[>] check:site-a: checking repository on s3-primary (read-data)\n", out.String())
}

func TestWriteRepairIndexStartText(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, writeRepairIndexStartText(&out, "site-a", "s3-primary"))
	assert.Equal(t, "[>] repair-index:site-a: repairing index on s3-primary\n", out.String())
}

func TestWriteRestoreStartText(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, writeRestoreStartText(&out, "site-a", "s3-primary", "latest", "/var/restore"))
	assert.Equal(t, "[>] restore:site-a: restoring snapshot latest from s3-primary to /var/restore\n", out.String())
}

func TestProgressHeartbeatLifecycle(t *testing.T) {
	var out bytes.Buffer
	hb := startProgressHeartbeat(&out, "check", "site-a", "checking")
	require.NotNil(t, hb)

	// Pause and Resume should not block or panic
	hb.Pause()
	// Repeated pause should be a no-op
	hb.Pause()

	hb.Resume()
	// Repeated resume should be a no-op
	hb.Resume()

	// Stop cleanly
	hb.Stop()
	// Repeated stop should be safe
	hb.Stop()
}

func TestProgressHeartbeatStopWhilePaused(t *testing.T) {
	var out bytes.Buffer
	hb := startProgressHeartbeat(&out, "restore", "site-a", "restoring")
	hb.Pause()
	hb.Stop()
}

func TestProgressHeartbeatNilSafe(t *testing.T) {
	var hb *progressHeartbeat
	hb.Pause()
	hb.Resume()
	hb.Stop()
}

type syncBuffer struct {
	bytes.Buffer
}

func TestProgressHeartbeatNonTTYTicker(t *testing.T) {
	// Heartbeat runs for short time without error
	var out syncBuffer
	hb := startProgressHeartbeat(&out, "check", "site-a", "checking")
	time.Sleep(10 * time.Millisecond)
	hb.Stop()
}
