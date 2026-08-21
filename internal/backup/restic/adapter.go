package restic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/process"
)

type Adapter struct {
	process process.ProcessRunner
}

func NewAdapter(runner process.ProcessRunner) *Adapter {
	if runner == nil {
		runner = process.NewProcessRunner()
	}
	return &Adapter{process: runner}
}

func (a *Adapter) Preflight() error {
	if _, err := a.process.LookPath("restic"); err != nil {
		return apperror.Hide("required restic binary is unavailable in PATH", err)
	}
	return nil
}

func (a *Adapter) EnsureRepository(ctx context.Context, repo RepoConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.Preflight(); err != nil {
		return err
	}

	var stdout, stderr bytes.Buffer
	args := []string{"init", "--repo", repo.URL, "--json"}

	err := a.process.Run(ctx, process.ProcessSpec{
		Command: "restic",
		Args:    args,
		Env:     a.env(repo),
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		errOutput := stderr.String()
		if isAlreadyInitialized(errOutput) {
			return nil
		}
		return apperror.Hide(fmt.Sprintf("could not initialize restic repository: %s", sanitize(errOutput)), err)
	}
	return nil
}

func (a *Adapter) BackupFiles(ctx context.Context, repo RepoConfig, spec BackupSpec) (SnapshotSummary, error) {
	if err := ctx.Err(); err != nil {
		return SnapshotSummary{}, err
	}
	if err := a.Preflight(); err != nil {
		return SnapshotSummary{}, err
	}
	if len(spec.Include) == 0 {
		return SnapshotSummary{}, errors.New("at least one include path is required for backup")
	}

	args := []string{"backup", "--repo", repo.URL, "--json"}
	for _, exclude := range spec.Exclude {
		if exclude != "" {
			args = append(args, "--exclude", exclude)
		}
	}
	for _, tag := range spec.Tags {
		if tag != "" {
			args = append(args, "--tag", tag)
		}
	}
	args = append(args, spec.Include...)

	var stdout, stderr bytes.Buffer
	err := a.process.Run(ctx, process.ProcessSpec{
		Command: "restic",
		Args:    args,
		Env:     a.env(repo),
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return SnapshotSummary{}, ctxErr
		}
		return SnapshotSummary{}, apperror.Hide(fmt.Sprintf("could not create restic snapshot: %s", sanitize(stderr.String())), err)
	}

	summary, parseErr := parseSnapshotSummary(stdout.Bytes())
	if parseErr != nil {
		return SnapshotSummary{}, apperror.Hide("could not parse restic summary", parseErr)
	}
	return summary, nil
}

func (a *Adapter) ApplyRetention(ctx context.Context, repo RepoConfig, keepLast int, siteName string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := a.Preflight(); err != nil {
		return 0, err
	}
	if keepLast < 1 {
		return 0, errors.New("keep_last must be at least 1")
	}

	args := []string{"forget", "--repo", repo.URL, "--keep-last", strconv.Itoa(keepLast), "--prune"}
	if siteName != "" {
		args = append(args, "--tag", "site:"+siteName)
	}

	var stdout, stderr bytes.Buffer
	err := a.process.Run(ctx, process.ProcessSpec{
		Command: "restic",
		Args:    args,
		Env:     a.env(repo),
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, ctxErr
		}
		return 0, apperror.Hide(fmt.Sprintf("could not apply restic retention: %s", sanitize(stderr.String())), err)
	}
	return parseReclaimed(stderr.String()), nil
}

// parseReclaimed extracts the reclaimed bytes from restic's prune output
// ("reclaimed 4.372 MiB"); 0 when the line is absent or unparseable.
func parseReclaimed(output string) int64 {
	match := reclaimedPattern.FindStringSubmatch(output)
	if match == nil {
		return 0
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0
	}
	switch strings.ToLower(match[2]) {
	case "b":
		return int64(value)
	case "kib":
		return int64(value * 1024)
	case "mib":
		return int64(value * 1024 * 1024)
	case "gib":
		return int64(value * 1024 * 1024 * 1024)
	case "tib":
		return int64(value * 1024 * 1024 * 1024 * 1024)
	default:
		return 0
	}
}

var reclaimedPattern = regexp.MustCompile(`reclaimed\s+([0-9.]+)\s+([A-Za-z]+)`)

// Unlock removes stale locks via the official binary (restic unlock
// semantics: stale locks only).
func (a *Adapter) Unlock(ctx context.Context, repo RepoConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.Preflight(); err != nil {
		return err
	}

	var stderr bytes.Buffer
	err := a.process.Run(ctx, process.ProcessSpec{
		Command: "restic",
		Args:    []string{"unlock", "--repo", repo.URL},
		Env:     a.env(repo),
		Stdout:  &stderr,
		Stderr:  &stderr,
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return apperror.Hide(fmt.Sprintf("could not unlock restic repository: %s", sanitize(stderr.String())), err)
	}
	return nil
}

func (a *Adapter) env(repo RepoConfig) []string {
	env := make([]string, 0, 4)
	if repo.Password != "" {
		env = append(env, "RESTIC_PASSWORD="+repo.Password)
	}
	if repo.AccessKeyID != "" {
		env = append(env, "AWS_ACCESS_KEY_ID="+repo.AccessKeyID)
	}
	if repo.SecretAccessKey != "" {
		env = append(env, "AWS_SECRET_ACCESS_KEY="+repo.SecretAccessKey)
	}
	if repo.Region != "" {
		env = append(env, "AWS_DEFAULT_REGION="+repo.Region)
	}
	return env
}

// RepositoryURL constructs the restic repository location for a storage target.
func RepositoryURL(storage config.Storage, siteName string) (string, error) {
	if siteName == "" {
		return "", errors.New("site name is required to build repository URL")
	}

	switch storage.Type {
	case "local":
		if storage.Directory == "" {
			return "", errors.New("local storage directory is required")
		}
		return filepath.Join(storage.Directory, "restic", siteName), nil

	case "s3", "r2":
		if storage.Bucket == "" {
			return "", errors.New("s3/r2 storage bucket is required")
		}
		var base string
		if storage.Endpoint != "" {
			endpoint := strings.TrimRight(storage.Endpoint, "/")
			if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
				endpoint = "https://" + endpoint
			}
			base = endpoint + "/" + storage.Bucket
		} else {
			base = "s3.amazonaws.com/" + storage.Bucket
		}

		subpath := "restic/" + siteName
		if storage.Prefix != "" {
			subpath = path.Join(storage.Prefix, subpath)
		}

		fullURL := base + "/" + subpath
		if !strings.HasPrefix(fullURL, "s3:") {
			return "s3:" + fullURL, nil
		}
		return fullURL, nil

	default:
		return "", fmt.Errorf("unsupported storage type %q for restic", storage.Type)
	}
}

func isAlreadyInitialized(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "already initialized") ||
		strings.Contains(lower, "already exists") ||
		strings.Contains(lower, "config file already exists")
}

func sanitize(input string) string {
	lines := strings.Split(strings.TrimSpace(input), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

func parseSnapshotSummary(output []byte) (SnapshotSummary, error) {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	var latestSummary SnapshotSummary
	found := false

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msgType, ok := msg["message_type"].(string); ok && msgType == "summary" {
			var summary SnapshotSummary
			if err := json.Unmarshal(line, &summary); err == nil {
				latestSummary = summary
				found = true
			}
		}
	}

	if !found {
		return SnapshotSummary{}, errors.New("no snapshot summary message found in restic output")
	}
	return latestSummary, nil
}
