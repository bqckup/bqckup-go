package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCLIProgressNonTerminalOutput(t *testing.T) {
	var buf bytes.Buffer
	p := NewCLIProgress(&buf)
	p.terminal = false // force non-terminal

	p.StartStage("compress files", -1)
	p.FinishStage()

	p.StartStage("upload s3-main", 1024*1024*50)
	p.Add(1024 * 1024 * 25)
	p.FinishStage()
	p.Done()

	output := buf.String()
	assert.Contains(t, output, "-> Compressing files...\n")
	assert.Contains(t, output, "-> Uploading to s3-main (50.0 MiB)\n")
	assert.NotContains(t, output, "\x1b[")
}

func TestCLIProgressTerminalFailAndCancelCleanup(t *testing.T) {
	var buf bytes.Buffer
	p := NewCLIProgress(&buf)
	p.terminal = true

	p.StartStage("upload s3-main", 1000)
	p.Add(400)
	p.FailStage()
	p.StartStage("upload secondary", 1000)
	p.Add(250)
	p.Done()

	output := buf.String()
	assert.Contains(t, output, "Uploading to secondary")
	assert.Contains(t, output, "\r\033[2K")
	assert.NotContains(t, output, "Uploading to s3-main\n")
}

func TestCLIProgressTerminalDeterminate(t *testing.T) {
	var buf bytes.Buffer
	p := NewCLIProgress(&buf)
	p.terminal = true // force terminal

	p.StartStage("upload s3-main", 1000)
	p.Add(500)
	p.Add(500)
	p.FinishStage()
	p.Done()

	output := buf.String()
	assert.Contains(t, output, "Uploading to s3-main")
	assert.Contains(t, output, "100%")
	assert.Contains(t, output, "ETA")
}

func TestCLIProgressTerminalShowsPercentBytesSpeedAndETA(t *testing.T) {
	var buf bytes.Buffer
	p := NewCLIProgress(&buf)
	p.terminal = true

	p.StartStage("upload s3-main", 1000)
	p.Add(600)
	p.renderTerminalLocked()

	output := buf.String()
	assert.Contains(t, output, "Uploading to s3-main")
	assert.Contains(t, output, "60%")
	assert.Contains(t, output, "600")
	assert.Contains(t, output, "ETA")
}

func TestCLIProgressTerminalClearsLineBeforeRedraw(t *testing.T) {
	var buf bytes.Buffer
	p := NewCLIProgress(&buf)
	p.terminal = true

	p.StartStage("upload s3-main", 1000)
	p.Add(400)
	p.renderTerminalLocked()
	p.Add(200)
	p.renderTerminalLocked()

	output := buf.String()
	assert.Contains(t, output, "\r\033[2K")
	assert.Contains(t, output, "Uploading to s3-main")
	assert.NotContains(t, output, "ETA --TA --")
}

func TestCLIProgressResetsPreviousStageBeforeStartingNextDestination(t *testing.T) {
	var buf bytes.Buffer
	p := NewCLIProgress(&buf)
	p.terminal = true

	p.StartStage("upload primary", 100)
	p.Add(40)
	p.StartStage("upload secondary", 100)

	output := buf.String()
	assert.Contains(t, output, "Uploading to secondary")
	assert.NotContains(t, output, "100%")
}

func TestCLIProgressFailStageCleanup(t *testing.T) {
	var buf bytes.Buffer
	p := NewCLIProgress(&buf)
	p.terminal = true

	p.StartStage("export test-db", -1)
	p.FailStage()
	p.Done()

	// Should have cleared line with \r\033[2K
	assert.Contains(t, buf.String(), "\r\033[2K")
}

func TestWriteProgressCleanupDoesNotAddBlankLine(t *testing.T) {
	var output bytes.Buffer

	writeProgressCleanup(&output, true)

	assert.Equal(t, "\r\033[2K", output.String())
}

func TestWriteProgressCleanupSkipsOutputWithoutRenderedFrame(t *testing.T) {
	var output bytes.Buffer

	writeProgressCleanup(&output, false)

	assert.Empty(t, output.String())
}
