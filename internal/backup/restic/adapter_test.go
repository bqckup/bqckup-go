package restic

import (
	"context"
	"errors"
	"testing"

	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeProcessRunner struct {
	lookPathErr error
	runs        []process.ProcessSpec
	runHandler  func(spec process.ProcessSpec) error
}

func (f *fakeProcessRunner) LookPath(command string) (string, error) {
	if f.lookPathErr != nil {
		return "", f.lookPathErr
	}
	return "/usr/bin/" + command, nil
}

func (f *fakeProcessRunner) Run(ctx context.Context, spec process.ProcessSpec) error {
	f.runs = append(f.runs, spec)
	if f.runHandler != nil {
		return f.runHandler(spec)
	}
	return nil
}

func TestRepositoryURL(t *testing.T) {
	t.Run("local storage", func(t *testing.T) {
		storage := config.Storage{Type: "local", Directory: "/var/backups/bqckup"}
		url, err := RepositoryURL(storage, "my-site")
		require.NoError(t, err)
		assert.Equal(t, "/var/backups/bqckup/restic/my-site", url)
	})

	t.Run("s3 storage with custom endpoint and prefix", func(t *testing.T) {
		storage := config.Storage{
			Type:     "s3",
			Endpoint: "https://minio.example.com",
			Bucket:   "backup-bucket",
			Prefix:   "prod",
		}
		url, err := RepositoryURL(storage, "my-site")
		require.NoError(t, err)
		assert.Equal(t, "s3:https://minio.example.com/backup-bucket/prod/restic/my-site", url)
	})

	t.Run("s3 storage with default endpoint and no prefix", func(t *testing.T) {
		storage := config.Storage{
			Type:   "s3",
			Bucket: "aws-bucket",
		}
		url, err := RepositoryURL(storage, "my-site")
		require.NoError(t, err)
		assert.Equal(t, "s3:s3.amazonaws.com/aws-bucket/restic/my-site", url)
	})

	t.Run("r2 storage", func(t *testing.T) {
		storage := config.Storage{
			Type:     "r2",
			Endpoint: "https://account-id.r2.cloudflarestorage.com",
			Bucket:   "media-vault",
			Prefix:   "servers/node1",
		}
		url, err := RepositoryURL(storage, "wordpress")
		require.NoError(t, err)
		assert.Equal(t, "s3:https://account-id.r2.cloudflarestorage.com/media-vault/servers/node1/restic/wordpress", url)
	})
}

func TestEnsureRepository(t *testing.T) {
	t.Run("fresh init runs restic init", func(t *testing.T) {
		runner := &fakeProcessRunner{}
		adapter := NewAdapter(runner)

		repo := RepoConfig{
			URL:      "/tmp/repo",
			Password: "secret-password",
		}
		err := adapter.EnsureRepository(context.Background(), repo)
		require.NoError(t, err)
		require.Len(t, runner.runs, 1)

		spec := runner.runs[0]
		assert.Equal(t, "restic", spec.Command)
		assert.Equal(t, []string{"init", "--repo", "/tmp/repo", "--json"}, spec.Args)
		assert.Contains(t, spec.Env, "RESTIC_PASSWORD=secret-password")
	})

	t.Run("already initialized repository is treated as success", func(t *testing.T) {
		runner := &fakeProcessRunner{
			runHandler: func(spec process.ProcessSpec) error {
				_, _ = spec.Stderr.Write([]byte("repository master key and config already initialized\n"))
				return errors.New("exit status 1")
			},
		}
		adapter := NewAdapter(runner)

		repo := RepoConfig{URL: "/tmp/repo", Password: "secret-password"}
		err := adapter.EnsureRepository(context.Background(), repo)
		require.NoError(t, err)
	})

	t.Run("missing restic binary returns preflight error", func(t *testing.T) {
		runner := &fakeProcessRunner{lookPathErr: errors.New("executable file not found in $PATH")}
		adapter := NewAdapter(runner)

		err := adapter.EnsureRepository(context.Background(), RepoConfig{URL: "/tmp/repo"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "required restic binary is unavailable")
	})
}

func TestBackupFiles(t *testing.T) {
	t.Run("successful backup parses json summary and passes arguments", func(t *testing.T) {
		runner := &fakeProcessRunner{
			runHandler: func(spec process.ProcessSpec) error {
				output := `{"message_type":"status","percent_done":0.5}
{"message_type":"summary","files_new":5,"files_changed":2,"files_unmodified":10,"total_files_processed":17,"total_bytes_processed":10485760,"data_added":204800,"total_duration":1.5,"snapshot_id":"abcdef12"}
`
				_, _ = spec.Stdout.Write([]byte(output))
				return nil
			},
		}
		adapter := NewAdapter(runner)

		repo := RepoConfig{
			URL:             "s3:https://s3.example.com/bucket/repo",
			Password:        "repo-pass",
			AccessKeyID:     "AKIAKEY",
			SecretAccessKey: "SECRETSECRET",
			Region:          "us-east-1",
		}
		spec := BackupSpec{
			SiteName: "site-a",
			Include:  []string{"/var/www/html", "/var/www/uploads"},
			Exclude:  []string{"*.tmp", "cache/**"},
			Tags:     []string{"bqckup", "site:site-a"},
		}

		summary, err := adapter.BackupFiles(context.Background(), repo, spec)
		require.NoError(t, err)
		assert.Equal(t, "abcdef12", summary.SnapshotID)
		assert.Equal(t, 5, summary.FilesNew)
		assert.Equal(t, 2, summary.FilesChanged)
		assert.Equal(t, int64(10485760), summary.TotalBytesProcessed)
		assert.Equal(t, int64(204800), summary.DataAdded)

		require.Len(t, runner.runs, 1)
		run := runner.runs[0]
		assert.Equal(t, "restic", run.Command)
		assert.Contains(t, run.Args, "--repo")
		assert.Contains(t, run.Args, "s3:https://s3.example.com/bucket/repo")
		assert.Contains(t, run.Args, "--exclude")
		assert.Contains(t, run.Args, "*.tmp")
		assert.Contains(t, run.Args, "--tag")
		assert.Contains(t, run.Args, "site:site-a")
		assert.Contains(t, run.Args, "/var/www/html")
		assert.Contains(t, run.Args, "/var/www/uploads")

		assert.Contains(t, run.Env, "RESTIC_PASSWORD=repo-pass")
		assert.Contains(t, run.Env, "AWS_ACCESS_KEY_ID=AKIAKEY")
		assert.Contains(t, run.Env, "AWS_SECRET_ACCESS_KEY=SECRETSECRET")
		assert.Contains(t, run.Env, "AWS_DEFAULT_REGION=us-east-1")
	})

	t.Run("context cancellation terminates immediately", func(t *testing.T) {
		runner := &fakeProcessRunner{}
		adapter := NewAdapter(runner)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := adapter.BackupFiles(ctx, RepoConfig{URL: "/tmp/repo"}, BackupSpec{Include: []string{"/data"}})
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestApplyRetention(t *testing.T) {
	runner := &fakeProcessRunner{}
	adapter := NewAdapter(runner)

	repo := RepoConfig{URL: "/tmp/repo", Password: "secret"}
	_, err := adapter.ApplyRetention(context.Background(), repo, 7, "my-site")
	require.NoError(t, err)

	require.Len(t, runner.runs, 1)
	spec := runner.runs[0]
	assert.Equal(t, []string{"forget", "--repo", "/tmp/repo", "--keep-last", "7", "--prune", "--tag", "site:my-site"}, spec.Args)
	assert.Contains(t, spec.Env, "RESTIC_PASSWORD=secret")
}

func TestUnlock(t *testing.T) {
	runner := &fakeProcessRunner{}
	adapter := NewAdapter(runner)

	repo := RepoConfig{URL: "/tmp/repo", Password: "secret"}
	err := adapter.Unlock(context.Background(), repo)
	require.NoError(t, err)

	require.Len(t, runner.runs, 1)
	spec := runner.runs[0]
	assert.Equal(t, []string{"unlock", "--repo", "/tmp/repo"}, spec.Args)
}
