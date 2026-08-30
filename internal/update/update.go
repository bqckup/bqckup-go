package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const defaultRepository = "bqckup/bqckup-go"

// Options controls a self-update from a GitHub release.
type Options struct {
	Version    string
	Repository string
	Target     string
	HTTPClient *http.Client
	Output     io.Writer
}

// Run downloads, verifies, and atomically installs the release binary.
func Run(ctx context.Context, options Options) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("self-update is currently supported on Linux only")
	}
	repository := options.Repository
	if repository == "" {
		repository = defaultRepository
	}
	target := options.Target
	if target == "" {
		var err error
		target, err = os.Executable()
		if err != nil {
			return fmt.Errorf("locate current binary: %w", err)
		}
	}
	target, err := filepath.EvalSymlinks(target)
	if err != nil {
		return fmt.Errorf("resolve current binary: %w", err)
	}

	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("unsupported Linux architecture %q", arch)
	}
	baseURL := "https://github.com/" + repository + "/releases"
	if options.Version == "" || options.Version == "latest" {
		baseURL += "/latest/download"
	} else {
		baseURL += "/download/" + options.Version
	}

	client := options.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	checksums, err := download(ctx, client, baseURL+"/checksums.txt")
	if err != nil {
		return fmt.Errorf("download release checksums: %w", err)
	}
	assetName, expected, err := releaseAsset(checksums, arch)
	if err != nil {
		return err
	}
	archive, err := download(ctx, client, baseURL+"/"+assetName)
	if err != nil {
		return fmt.Errorf("download release %s: %w", assetName, err)
	}
	actual := sha256.Sum256(archive)
	if !strings.EqualFold(hex.EncodeToString(actual[:]), expected) {
		return fmt.Errorf("release checksum mismatch: expected %s, got %x", expected, actual)
	}

	installed, err := extractBinary(archive)
	if err != nil {
		return err
	}
	if err := installAtomic(target, installed); err != nil {
		return err
	}
	if options.Output != nil {
		_, _ = fmt.Fprintf(options.Output, "updated bqckup at %s\n", target)
	}
	return nil
}

func download(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP status %s", response.Status)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func releaseAsset(checksums []byte, arch string) (string, string, error) {
	suffix := "_linux_" + arch + ".tar.gz"
	var assetName, expected string
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasSuffix(fields[1], suffix) {
			continue
		}
		if assetName != "" {
			return "", "", errors.New("release checksums contain multiple matching Linux assets")
		}
		if len(fields[0]) != sha256.Size*2 {
			return "", "", fmt.Errorf("invalid SHA-256 checksum for release asset %q", fields[1])
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return "", "", fmt.Errorf("invalid SHA-256 checksum for release asset %q", fields[1])
		}
		assetName, expected = fields[1], fields[0]
	}
	if assetName == "" {
		return "", "", fmt.Errorf("no Linux %s release asset found", arch)
	}
	return assetName, expected, nil
}

func extractBinary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open release archive: %w", err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read release archive: %w", err)
		}
		if header.Name != "bqckup" || header.Typeflag != tar.TypeReg {
			continue
		}
		if header.Size < 0 || header.Size > 256*1024*1024 {
			return nil, errors.New("release binary has an invalid size")
		}
		binary, err := io.ReadAll(io.LimitReader(reader, header.Size))
		if err != nil {
			return nil, fmt.Errorf("read release binary: %w", err)
		}
		if int64(len(binary)) != header.Size {
			return nil, errors.New("release binary is truncated")
		}
		return binary, nil
	}
	return nil, errors.New("release archive does not contain bqckup binary")
}

func installAtomic(target string, binary []byte) error {
	if len(binary) == 0 {
		return errors.New("release binary is empty")
	}
	directory := filepath.Dir(target)
	temporary, err := os.CreateTemp(directory, ".bqckup-update-*")
	if err != nil {
		return fmt.Errorf("create update file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o755); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set update permissions: %w", err)
	}
	if _, err := temporary.Write(binary); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write update file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync update file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close update file: %w", err)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return fmt.Errorf("install updated binary: %w", err)
	}
	return nil
}
