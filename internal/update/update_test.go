package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingProgress struct {
	stages []string
	added  int64
	done   bool
}

func (r *recordingProgress) StartStage(label string, total int64) {
	r.stages = append(r.stages, label)
}
func (r *recordingProgress) Add(units int64) {
	r.added += units
}
func (r *recordingProgress) FinishStage() {}
func (r *recordingProgress) FailStage()   {}
func (r *recordingProgress) Done() {
	r.done = true
}

func createTestArchive(t *testing.T, binaryContent []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	header := &tar.Header{
		Name:     "bqckup",
		Mode:     0o755,
		Size:     int64(len(binaryContent)),
		Typeflag: tar.TypeReg,
	}
	require.NoError(t, tw.WriteHeader(header))
	_, err := tw.Write(binaryContent)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())

	return buf.Bytes()
}

func TestInstallAtomicPermissionDeniedHint(t *testing.T) {
	targetDir := t.TempDir()
	require.NoError(t, os.Chmod(targetDir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(targetDir, 0o755) })

	target := filepath.Join(targetDir, "bqckup")
	err := installAtomic(target, []byte("updated"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
	assert.Contains(t, err.Error(), "sudo")
	assert.Contains(t, err.Error(), targetDir)
}

func TestUpdateRunWithProgress(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("self-update supported on Linux only")
	}

	newBinary := []byte("#!/bin/sh\necho updated\n")
	archiveBytes := createTestArchive(t, newBinary)
	archiveSHA := sha256.Sum256(archiveBytes)
	archiveSHAHex := hex.EncodeToString(archiveSHA[:])
	arch := runtime.GOARCH
	assetName := fmt.Sprintf("bqckup_test_linux_%s.tar.gz", arch)
	checksumsContent := fmt.Sprintf("%s  %s\n", archiveSHAHex, assetName)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/test/repo/releases/latest/download/checksums.txt":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(checksumsContent))
		case "/test/repo/releases/latest/download/" + assetName:
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(archiveBytes)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(archiveBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	targetDir := t.TempDir()
	targetFile := filepath.Join(targetDir, "bqckup")
	require.NoError(t, os.WriteFile(targetFile, []byte("old"), 0o755))

	progress := &recordingProgress{}
	var out bytes.Buffer

	// Custom client to redirect GitHub release URLs to our test server
	transport := &customTransport{
		serverURL: server.URL,
		transport: http.DefaultTransport,
	}
	client := &http.Client{Transport: transport}

	err := Run(context.Background(), Options{
		Repository: "test/repo",
		Target:     targetFile,
		HTTPClient: client,
		Output:     &out,
		Progress:   progress,
	})
	require.NoError(t, err)

	assert.Contains(t, out.String(), "updated bqckup at "+targetFile)
	assert.True(t, progress.done)
	assert.Contains(t, progress.stages, "downloading release")
	assert.Contains(t, progress.stages, "verifying checksum")
	assert.Contains(t, progress.stages, "installing update")
	assert.Equal(t, int64(len(archiveBytes))+2, progress.added)

	content, err := os.ReadFile(targetFile)
	require.NoError(t, err)
	assert.Equal(t, newBinary, content)
}

type customTransport struct {
	serverURL string
	transport http.RoundTripper
}

func (t *customTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite request URL to point to test server
	req.URL.Scheme = "http"
	if req.URL.Host == "github.com" {
		u, _ := req.URL.Parse(t.serverURL + req.URL.Path)
		req.URL = u
	}
	return t.transport.RoundTrip(req)
}
