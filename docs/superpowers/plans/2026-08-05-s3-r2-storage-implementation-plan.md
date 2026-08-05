# S3 and R2 Storage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add verified, non-overwriting local, S3-compatible, and Cloudflare R2 destinations with safe inline runtime credentials, primary fallback, and prefix-scoped retention.

**Architecture:** Viper remains confined to `internal/config`, which loads the unversioned storage document and resolves primary destinations. `internal/storage/s3compat` wraps AWS SDK for Go v2 plus S3 Transfer Manager behind adapter-owned interfaces; `internal/app` constructs concrete adapters while `internal/backup` remains SDK-independent. The existing storage contract drives upload, paginated listing, and bounded prefix deletion.

**Tech Stack:** Go 1.26, Cobra, Viper, AWS SDK for Go v2, S3 Transfer Manager, GORM SQLite, Testify, YAML schema v2.

## Global Constraints

- Root and site documents remain schema version 2; the storage document has no `version` field.
- Accept exactly one of `config/storages.yaml` and `config/storages.yml`; reject both together.
- Supported storage types are exactly `local`, `s3`, and `r2`.
- Real inline credentials are allowed only in a non-symlink storage file with mode `0600`.
- Never expose credentials, endpoints, request URLs, signed headers, provider bodies, or child environments in output, JSON, history, logs, fixtures, or errors rendered to users.
- Existing objects are never overwritten; every upload uses `If-None-Match: *`.
- All configured destinations are required; retention starts only after all uploads and artifact history writes succeed.
- Remote listing and deletion never escape the configured prefix plus `bqckup/<site>/`.
- Every network operation receives `context.Context`; SDK retries are limited to three attempts.
- Default tests are deterministic and require no network or real credentials.
- Database repair, restore, bucket creation, environment credential references, remote credential providers, Restic, and Rustic remain out of scope.

---

### Task 1: Load the unversioned storage document and resolve primary destinations

**Files:**
- Modify: `internal/config/types.go`
- Modify: `internal/config/load.go`
- Modify: `internal/config/load_test.go`
- Modify: `internal/config/validate_test.go`

**Interfaces:**
- Consumes: existing `Load(ctx context.Context, dir string) (Config, error)`.
- Produces: `Storage.Primary bool`, unversioned storage discovery, `Config.PrimaryStorage() (string, bool)`, and resolved site destinations.

- [ ] **Step 1: Write failing storage discovery and primary tests**

Add tests proving `.yaml` and `.yml` load, both together fail, a storage-level `version` is unknown, and a site without destinations receives the single primary storage:

```go
func TestLoadAcceptsUnversionedStorageYAMLAndResolvesPrimary(t *testing.T) {
	dir := writeConfigTree(t, validRootYAML, `storages:
  local-primary:
    type: local
    directory: /var/backups/bqckup
    primary: true
`, validSiteWithoutDestinationsYAML(t))

	cfg, err := Load(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, []Destination{{Storage: "local-primary"}}, cfg.Sites[0].Destinations)
}

func TestLoadRejectsAmbiguousStorageExtensions(t *testing.T) {
	dir := writeConfigTree(t, validRootYAML, localStorageYAML, validSiteYAML(t))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config", "storages.yml"), []byte(localStorageYAML), 0o600))

	_, err := Load(context.Background(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both storages.yaml and storages.yml")
}
```

- [ ] **Step 2: Run the focused config tests and verify RED**

Run: `go test ./internal/config -run 'TestLoad(AcceptsUnversioned|RejectsAmbiguous)' -count=1`

Expected: FAIL because the loader still requires `version: 2`, only reads `storages.yaml`, and does not resolve primary storage.

- [ ] **Step 3: Implement storage discovery and primary resolution**

Change the storage document and configuration types:

```go
type Config struct {
	Version  int
	App      App
	Storages map[string]Storage
	Sites    []Site
}

type Storage struct {
	Type            string `mapstructure:"type" yaml:"type"`
	Directory       string `mapstructure:"directory" yaml:"directory"`
	Bucket          string `mapstructure:"bucket" yaml:"bucket"`
	AccessKeyID     string `mapstructure:"access_key_id" yaml:"access_key_id"`
	SecretAccessKey string `mapstructure:"secret_access_key" yaml:"secret_access_key"`
	Region          string `mapstructure:"region" yaml:"region"`
	Endpoint        string `mapstructure:"endpoint" yaml:"endpoint"`
	Prefix          string `mapstructure:"prefix" yaml:"prefix"`
	Primary         bool   `mapstructure:"primary" yaml:"primary"`
}

type storageDocument struct {
	Storages map[string]Storage `mapstructure:"storages"`
}
```

Add deterministic discovery and primary selection:

```go
func storagePath(dir string) (string, error) {
	yamlPath := filepath.Join(dir, "config", "storages.yaml")
	ymlPath := filepath.Join(dir, "config", "storages.yml")
	_, yamlErr := os.Lstat(yamlPath)
	_, ymlErr := os.Lstat(ymlPath)
	if yamlErr == nil && ymlErr == nil {
		return "", &Error{File: filepath.Join(dir, "config"), Kind: ErrorRead, Err: errors.New("both storages.yaml and storages.yml exist")}
	}
	if yamlErr == nil { return yamlPath, nil }
	if ymlErr == nil { return ymlPath, nil }
	return yamlPath, nil
}

func (c Config) PrimaryStorage() (string, bool) {
	names := make([]string, 0, len(c.Storages))
	for name, store := range c.Storages {
		if store.Primary { names = append(names, name) }
	}
	sort.Strings(names)
	if len(names) != 1 { return "", false }
	return names[0], true
}
```

After loading sites, apply `region: auto` to R2 entries and fill an empty destination list from `PrimaryStorage` before calling `Validate`.

- [ ] **Step 4: Run all config tests and verify GREEN**

Run: `go test ./internal/config -count=1`

Expected: PASS with `.yaml`, `.yml`, ambiguity, unversioned document, and primary fallback covered.

- [ ] **Step 5: Commit the loader slice**

```bash
git add internal/config/types.go internal/config/load.go internal/config/load_test.go internal/config/validate_test.go
git commit -m "feat: load typed storage backends"
```

---

### Task 2: Validate backend-specific fields, endpoints, and credential-file permissions

**Files:**
- Modify: `internal/config/load.go`
- Modify: `internal/config/validate.go`
- Create: `internal/config/storage_validation_test.go`

**Interfaces:**
- Consumes: `Storage` fields and selected storage path from Task 1.
- Produces: strict local/S3/R2 validation and `validateCredentialFile(path string, storages map[string]Storage) error`.

- [ ] **Step 1: Write table-driven failing validation tests**

Cover valid local, S3, and R2 entries plus missing fields, cross-type fields, multiple primaries, endpoint URL safety, unsafe prefix, symlink, and permission mode:

```go
func TestValidateStorageTypes(t *testing.T) {
	tests := []struct {
		name    string
		storage Storage
		wantErr string
	}{
		{name: "local", storage: Storage{Type: "local", Directory: "/var/backups"}},
		{name: "s3", storage: Storage{Type: "s3", Bucket: "backups", AccessKeyID: "example", SecretAccessKey: "example", Region: "us-east-1"}},
		{name: "r2", storage: Storage{Type: "r2", Bucket: "backups", AccessKeyID: "example", SecretAccessKey: "example", Region: "auto", Endpoint: "https://account.r2.cloudflarestorage.com"}},
		{name: "local rejects bucket", storage: Storage{Type: "local", Directory: "/var/backups", Bucket: "unexpected"}, wantErr: "bucket is not valid for local storage"},
		{name: "r2 requires endpoint", storage: Storage{Type: "r2", Bucket: "backups", AccessKeyID: "example", SecretAccessKey: "example", Region: "auto"}, wantErr: "endpoint is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig(t)
			cfg.Storages = map[string]Storage{"testing": test.storage}
			cfg.Sites[0].Destinations = []Destination{{Storage: "testing"}}
			err := cfg.Validate()
			if test.wantErr == "" { require.NoError(t, err); return }
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantErr)
		})
	}
}
```

- [ ] **Step 2: Run the new tests and verify RED**

Run: `go test ./internal/config -run 'TestValidateStorage|TestLoadRejectsCredential' -count=1`

Expected: FAIL because only local storage is accepted and storage-file security is not enforced.

- [ ] **Step 3: Implement exact backend validation**

Split validation into focused helpers:

```go
func validateStorage(name string, value Storage) error
func validateLocalStorage(field string, value Storage) error
func validateS3Storage(field string, value Storage) error
func validateR2Storage(field string, value Storage) error
func validateEndpoint(field, raw string, required bool) error
func validatePrefix(field, prefix string) error
```

Require HTTPS except loopback HTTP, reject URL user info/query/fragment, require one primary at most, and report only field paths—not values. Local rejects all remote-only fields. S3 requires bucket, access key, secret, and region. R2 requires bucket, both keys, HTTPS endpoint, and normalized region `auto`.

- [ ] **Step 4: Implement credential-file checks in `Load`**

Use `os.Lstat`, reject symlinks, and require exact owner read/write permissions when any entry has inline keys:

```go
func validateCredentialFile(path string, stores map[string]Storage) error {
	hasCredentials := false
	for _, store := range stores {
		hasCredentials = hasCredentials || store.AccessKeyID != "" || store.SecretAccessKey != ""
	}
	if !hasCredentials { return nil }
	info, err := os.Lstat(path)
	if err != nil { return &Error{File: path, Kind: ErrorRead, Err: err} }
	if info.Mode()&os.ModeSymlink != 0 {
		return validationError(path, "storages", "credential-bearing storage file must not be a symlink")
	}
	if info.Mode().Perm() != 0o600 {
		return validationError(path, "storages", "credential-bearing storage file must have mode 0600")
	}
	return nil
}
```

- [ ] **Step 5: Run config tests and verify GREEN**

Run: `go test ./internal/config -count=1`

Expected: PASS with secrets/endpoints absent from every asserted error string.

- [ ] **Step 6: Commit validation and file security**

```bash
git add internal/config/load.go internal/config/validate.go internal/config/storage_validation_test.go
git commit -m "feat: validate remote storage configuration"
```

---

### Task 3: Share portable object-key validation

**Files:**
- Create: `internal/storage/key.go`
- Create: `internal/storage/key_test.go`
- Modify: `internal/storage/local/local.go`
- Modify: `internal/storage/local/local_test.go`

**Interfaces:**
- Produces: `storage.ValidateKey(key string) error` and `storage.JoinPrefix(prefix, key string) (string, error)`.
- Consumes: local adapter keys now; S3-compatible adapter keys in later tasks.

- [ ] **Step 1: Write failing portable key tests**

```go
func TestValidateKey(t *testing.T) {
	for _, key := range []string{"bqckup/site/2026-08-05T00-00-00Z/files.tar.gz", "company/bqckup/site"} {
		require.NoError(t, ValidateKey(key))
	}
	for _, key := range []string{"", "/absolute", "../escape", "safe/../escape", `safe\\escape`, "safe//empty"} {
		require.Error(t, ValidateKey(key))
	}
}

func TestJoinPrefix(t *testing.T) {
	key, err := JoinPrefix("company", "bqckup/site/file")
	require.NoError(t, err)
	assert.Equal(t, "company/bqckup/site/file", key)
}
```

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test ./internal/storage ./internal/storage/local -run 'Test(ValidateKey|JoinPrefix|PutRejectsUnsafeKeys)' -count=1`

Expected: FAIL because the shared functions do not exist.

- [ ] **Step 3: Implement shared validation and reuse it locally**

```go
func ValidateKey(key string) error {
	if key == "" || strings.Contains(key, "\\") || path.IsAbs(key) || path.Clean(key) != key {
		return fmt.Errorf("unsafe storage key %q", key)
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("unsafe storage key %q", key)
		}
	}
	return nil
}

func JoinPrefix(prefix, key string) (string, error) {
	if err := ValidateKey(key); err != nil { return "", err }
	if prefix == "" { return key, nil }
	if err := ValidateKey(prefix); err != nil { return "", err }
	return path.Join(prefix, key), nil
}
```

Call `storage.ValidateKey` at the start of `local.Store.resolve`, retaining the filesystem escape check.

- [ ] **Step 4: Run storage tests and verify GREEN**

Run: `go test ./internal/storage ./internal/storage/local -count=1`

Expected: PASS with unchanged local behavior.

- [ ] **Step 5: Commit shared key safety**

```bash
git add internal/storage/key.go internal/storage/key_test.go internal/storage/local/local.go internal/storage/local/local_test.go
git commit -m "refactor: share storage key validation"
```

---

### Task 4: Implement verified S3-compatible uploads

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/storage/s3compat/client.go`
- Create: `internal/storage/s3compat/store.go`
- Create: `internal/storage/s3compat/store_test.go`

**Interfaces:**
- Produces: `s3compat.Provider`, `s3compat.Options`, `s3compat.New(ctx, options) (*Store, error)`, and a `storage.Store` implementation.
- Consumes: `storage.Artifact`, `storage.StoredArtifact`, `storage.ValidateKey`, and `storage.JoinPrefix`.

- [ ] **Step 1: Add pinned AWS SDK modules**

Run:

```bash
go get github.com/aws/aws-sdk-go-v2/config@latest \
  github.com/aws/aws-sdk-go-v2/credentials@latest \
  github.com/aws/aws-sdk-go-v2/service/s3@latest \
  github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager@latest
go mod tidy
```

Expected: `go.mod` records direct AWS SDK dependencies and `go mod tidy` completes successfully.

- [ ] **Step 2: Write failing upload contract tests with adapter-owned fakes**

Define the wished-for options and assert conditional upload plus verification:

```go
func TestPutUploadsConditionallyAndVerifiesMetadata(t *testing.T) {
	artifact := sourceArtifact(t, []byte("verified backup"))
	uploader := &fakeUploader{}
	client := &fakeClient{headOutput: &s3.HeadObjectOutput{
		ContentLength: aws.Int64(artifact.Size),
		Metadata: map[string]string{"bqckup-sha256": artifact.SHA256, "bqckup-size": strconv.FormatInt(artifact.Size, 10)},
	}}
	store := newWithClients(Options{Provider: ProviderS3, Bucket: "backups", Region: "us-east-1"}, uploader, client)

	stored, err := store.Put(context.Background(), artifact, "bqckup/site/2026-08-05T00-00-00Z/files.tar.gz")
	require.NoError(t, err)
	assert.Equal(t, aws.String("*"), uploader.input.IfNoneMatch)
	assert.Equal(t, artifact.SHA256, stored.SHA256)
	assert.Equal(t, artifact.Size, stored.Size)
}
```

Also add tests for local checksum mismatch, cancelled context, collision response, head mismatch, and exact-object cleanup after failed verification.

- [ ] **Step 3: Run upload tests and verify RED**

Run: `go test ./internal/storage/s3compat -run 'TestPut' -count=1`

Expected: FAIL because the package and adapter do not exist.

- [ ] **Step 4: Implement client construction**

Create exported adapter options and provider constants:

```go
type Provider string
const (
	ProviderS3 Provider = "s3"
	ProviderR2 Provider = "r2"
)

type Options struct {
	Provider        Provider
	Bucket          string
	Region          string
	Endpoint        string
	Prefix          string
	AccessKeyID     string
	SecretAccessKey string
}
```

`New` uses `config.LoadDefaultConfig`, `credentials.NewStaticCredentialsProvider`, `retry.NewStandard(func(o *retry.StandardOptions) { o.MaxAttempts = 3 })`, `s3.NewFromConfig`, `BaseEndpoint`, and `UsePathStyle` for custom endpoints. Wrap the raw client with `transfermanager.New` and pass both clients to `newWithClients`.

- [ ] **Step 5: Implement preflight verification and upload**

Before network I/O, read the artifact once to compute SHA-256 and size and compare both with `storage.Artifact`. Reopen the file for `transfermanager.Client.UploadObject`:

```go
output, err := s.uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
	Bucket:        aws.String(s.bucket),
	Key:           aws.String(finalKey),
	Body:          file,
	IfNoneMatch:   aws.String("*"),
	MpuObjectSize: aws.Int64(artifact.Size),
	Metadata: map[string]string{
		"bqckup-sha256": strings.ToLower(artifact.SHA256),
		"bqckup-size": strconv.FormatInt(artifact.Size, 10),
	},
})
```

Call `HeadObject`, compare `ContentLength` and both metadata values, and call exact `DeleteObject` on verification failure. Preserve `context.Canceled`; map HTTP 412 or API code `PreconditionFailed` to a stable collision error without rendering provider details.

- [ ] **Step 6: Run upload tests and verify GREEN**

Run: `go test ./internal/storage/s3compat -run 'TestPut' -count=1`

Expected: PASS for success, collision, checksum mismatch, cancellation, verification, and cleanup.

- [ ] **Step 7: Commit verified uploads**

```bash
git add go.mod go.sum internal/storage/s3compat/client.go internal/storage/s3compat/store.go internal/storage/s3compat/store_test.go
git commit -m "feat: upload verified S3 artifacts"
```

---

### Task 5: Implement paginated remote retention

**Files:**
- Modify: `internal/storage/s3compat/store.go`
- Create: `internal/storage/s3compat/retention_test.go`

**Interfaces:**
- Produces: `(*Store).ListBackupSets(ctx, sitePrefix)` and `(*Store).Delete(ctx, backupSetPrefix)`.
- Consumes: `storage.TimestampLayout` and the existing `retention.Apply` contract.

- [ ] **Step 1: Write failing pagination and deletion tests**

Add tests with two `ListObjectsV2` pages, valid/invalid timestamps, foreign keys, empty results, more than 1,000 delete identifiers, cancellation, and a partial delete error:

```go
func TestListBackupSetsPaginatesAndFilters(t *testing.T) {
	client := &fakeClient{listOutputs: []*s3.ListObjectsV2Output{
		{Contents: []types.Object{{Key: aws.String("company/bqckup/site/2026-08-01T00-00-00Z/files.tar.gz")}}, IsTruncated: aws.Bool(true), NextContinuationToken: aws.String("next")},
		{Contents: []types.Object{
			{Key: aws.String("company/bqckup/site/2026-08-02T00-00-00Z/files.tar.gz")},
			{Key: aws.String("company/bqckup/other/2026-08-03T00-00-00Z/files.tar.gz")},
		}},
	}}
	store := newWithClients(Options{Bucket: "backups", Prefix: "company"}, &fakeUploader{}, client)

	sets, err := store.ListBackupSets(context.Background(), "bqckup/site")
	require.NoError(t, err)
	assert.Len(t, sets, 2)
	assert.Equal(t, aws.String("next"), client.listInputs[1].ContinuationToken)
}
```

- [ ] **Step 2: Run retention adapter tests and verify RED**

Run: `go test ./internal/storage/s3compat -run 'Test(ListBackupSets|Delete)' -count=1`

Expected: FAIL because the methods do not yet implement remote behavior.

- [ ] **Step 3: Implement prefix-scoped listing**

Join the configured prefix with `sitePrefix + "/"`, paginate until `IsTruncated` is false, strip the configured prefix, extract the timestamp segment immediately after the site prefix, parse it with `storage.TimestampLayout`, deduplicate backup-set keys, and return them oldest first.

Do not request or inspect keys outside the precise prefix supplied to `ListObjectsV2`.

- [ ] **Step 4: Implement bounded prefix deletion**

Validate that `backupSetPrefix` has the exact shape `bqckup/<safe-site>/<valid-UTC-timestamp>`. List only `finalPrefix + "/"`, collect object identifiers, and call `DeleteObjects` in batches of at most 1,000. Treat any returned per-object error as failure and stop before requesting another page.

- [ ] **Step 5: Run adapter and generic retention tests and verify GREEN**

Run: `go test ./internal/storage/s3compat ./internal/retention -count=1`

Expected: PASS with paginated, prefix-isolated list/delete behavior.

- [ ] **Step 6: Commit remote retention**

```bash
git add internal/storage/s3compat/store.go internal/storage/s3compat/retention_test.go
git commit -m "feat: retain S3 backup sets safely"
```

---

### Task 6: Wire all storage types into the application and CLI templates

**Files:**
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_test.go`
- Modify: `internal/backup/runner_test.go`
- Modify: `internal/cli/init.go`
- Modify: `internal/cli/commands_test.go`

**Interfaces:**
- Consumes: validated `config.Storage`, `local.New`, and `s3compat.New`.
- Produces: `buildStores(ctx context.Context, configured map[string]config.Storage) (map[string]storage.Store, error)`.

- [ ] **Step 1: Write failing app-construction and init-template tests**

Test local construction, S3 construction without network I/O, R2 construction with `auto`, unsupported type rejection, mixed runner destinations, redaction of a provider error containing credential and endpoint canaries, and the unversioned init storage file:

```go
func TestInitCreatesUnversionedPrimaryLocalStorage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bqckup")
	root, _, _ := commandForTest(t, "--config-dir", dir, "init")
	require.NoError(t, root.Execute())
	contents, err := os.ReadFile(filepath.Join(dir, "config", "storages.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(contents), "version:")
	assert.Contains(t, string(contents), "type: local")
	assert.Contains(t, string(contents), "primary: true")
}

func TestRunnerRedactsRemoteProviderFailure(t *testing.T) {
	deps := successfulDependencies(t)
	deps.stores["local-primary"].(*fakeStore).putErr = errors.New("ACCESS_CANARY SECRET_CANARY https://endpoint.invalid")
	result, err := NewRunner(deps.dependencies()).Run(context.Background(), validSite(), false)
	require.Error(t, err)
	assert.Equal(t, StatusFailed, result.Status)
	assert.Equal(t, "could not store backup artifact", deps.repository.errorMessage)
	assert.NotContains(t, apperror.UserMessage(err), "CANARY")
	assert.NotContains(t, apperror.UserMessage(err), "endpoint.invalid")
}
```

- [ ] **Step 2: Run focused app and CLI tests and verify RED**

Run: `go test ./internal/app ./internal/backup ./internal/cli -run 'Test(OpenWires|RunnerRequiresEveryDestination|InitCreatesUnversioned)' -count=1`

Expected: FAIL because app wiring accepts only local and init still writes a versioned storage document.

- [ ] **Step 3: Extract concrete store construction in `internal/app`**

```go
func buildStores(ctx context.Context, configured map[string]config.Storage) (map[string]storage.Store, error) {
	stores := make(map[string]storage.Store, len(configured))
	for name, value := range configured {
		var store storage.Store
		var err error
		switch value.Type {
		case "local":
			store, err = localstorage.New(value.Directory)
		case "s3", "r2":
			store, err = s3compat.New(ctx, s3compat.Options{
				Provider: s3compat.Provider(value.Type), Bucket: value.Bucket,
				Region: value.Region, Endpoint: value.Endpoint, Prefix: value.Prefix,
				AccessKeyID: value.AccessKeyID, SecretAccessKey: value.SecretAccessKey,
			})
		default:
			err = errors.New("unsupported storage type")
		}
		if err != nil { return nil, apperror.Wrap(apperror.CategoryPreflight, "could not prepare a storage destination", err) }
		stores[name] = store
	}
	return stores, nil
}
```

Call this from `Open` after history migration and close the database on construction failure.

- [ ] **Step 4: Update init and all test fixtures to the new document**

The init storage file becomes:

```yaml
storages:
  local-primary:
    type: local
    directory: /var/backups/bqckup
    primary: true
```

Remove `version: 2` from storage fixtures while retaining it in root and site fixtures.

- [ ] **Step 5: Run app, runner, and CLI tests and verify GREEN**

Run: `go test ./internal/app ./internal/backup ./internal/cli -count=1`

Expected: PASS with local behavior unchanged, remote clients constructible, mixed destinations all-required, and unversioned init output.

- [ ] **Step 6: Commit application wiring**

```bash
git add internal/app/app.go internal/app/app_test.go internal/backup/runner_test.go internal/cli/init.go internal/cli/commands_test.go
git commit -m "feat: wire S3 and R2 destinations"
```

---

### Task 7: Update canonical docs, examples, checks, and repository skill

**Files:**
- Modify: `README.md`
- Modify: `configs/config/storages.yaml`
- Modify: `configs/config/storages.example.yaml`
- Modify: `docs/architecture.md`
- Modify: `docs/configuration-v2.md`
- Modify: `docs/testing.md`
- Modify: `docs/intern-backlog.md`
- Modify: `scripts/check-docs.sh`
- Modify: `.agents/skills/developing-bqckup-go/SKILL.md`
- Modify: `.agents/skills/developing-bqckup-go/references/architecture.md`
- Modify: `.agents/skills/developing-bqckup-go/references/config-v2.md`

**Interfaces:**
- Consumes: the implemented public config and adapter behavior.
- Produces: English contributor documentation and automated checks that reject real committed credentials.

- [ ] **Step 1: Write a failing documentation contract for the new public surface**

Extend `scripts/check-docs.sh` to require `type: local`, `type: s3`, `type: r2`, `primary:`, `access_key_id`, `secret_access_key`, `chmod 600`, and `configs/config/storages.example.yaml`. Reject inline credentials unless their values start with `EXAMPLE_`:

```sh
credential_lines=$(grep -RniE '^[[:space:]]*(access_key_id|secret_access_key):[[:space:]]*[^[:space:]]' configs || true)
if [ -n "$credential_lines" ] && printf '%s\n' "$credential_lines" | grep -vE ':[[:space:]]*EXAMPLE_[A-Z0-9_]+$'; then
    echo "example configuration contains a non-example inline credential" >&2
    failed=1
fi
```

- [ ] **Step 2: Run the documentation check and verify RED**

Run: `sh scripts/check-docs.sh`

Expected: FAIL until canonical docs and the runtime local example match the new contract.

- [ ] **Step 3: Update all canonical documentation and examples**

Document exact fields/defaults, `.yaml`/`.yml` ambiguity, primary fallback, mode `0600`, S3 custom endpoints, R2 `auto`, conditional uploads, multipart cancellation, pagination, and prefix-safe retention. Mark the user-approved combined M04/M07 slice and keep database repair and Restic deferred.

Change `configs/config/storages.yaml` to the unversioned primary local example. Keep only `EXAMPLE_` values in `storages.example.yaml`. Update the repository skill to replace the obsolete environment-only rule with the approved runtime-only inline credential contract and its permission/redaction requirements.

- [ ] **Step 4: Run docs and language checks and verify GREEN**

Run:

```bash
sh scripts/check-docs.sh
find README.md docs .agents/skills -type f -name '*.md' -print0 | \
  xargs -0 grep -Eni '\b(berbasis|dengan|dan|untuk|yang|tidak|dari|konfigurasi|penyimpanan)\b' && exit 1 || true
```

Expected: documentation check exits 0 and the English-only scan prints nothing.

- [ ] **Step 5: Commit documentation and skill updates**

```bash
git add README.md configs docs scripts/check-docs.sh .agents/skills/developing-bqckup-go
git commit -m "docs: document S3 and R2 storage"
```

---

### Task 8: Add opt-in disposable provider verification and complete the PR

**Files:**
- Create: `internal/storage/s3compat/integration_test.go`
- Modify: `docs/testing.md`

**Interfaces:**
- Consumes: `BQCKUP_S3_INTEGRATION_CONFIG` as a path to a private runtime config and `BQCKUP_S3_INTEGRATION_STORAGE` as a storage name; credentials remain inside the mode-`0600` file.
- Produces: build-tagged live verification of put, head verification, list, and delete.

- [ ] **Step 1: Write the build-tagged integration test**

Use external package `s3compat_test`, skip unless both non-secret selector variables exist, load the config through `config.Load`, construct the selected adapter, and use a unique key below `bqckup/integration-<UUID>/<timestamp>/probe.txt`. Register cleanup before upload and verify `Put`, `ListBackupSets`, then `Delete`.

```go
//go:build integration

func TestDisposableS3CompatibleStorage(t *testing.T) {
	configDir := os.Getenv("BQCKUP_S3_INTEGRATION_CONFIG")
	storageName := os.Getenv("BQCKUP_S3_INTEGRATION_STORAGE")
	if configDir == "" || storageName == "" { t.Skip("S3 integration config is not selected") }
	ctx := context.Background()
	cfg, err := config.Load(ctx, configDir)
	require.NoError(t, err)
	selected, ok := cfg.Storages[storageName]
	require.True(t, ok)
	store, err := s3compat.New(ctx, s3compat.Options{
		Provider: s3compat.Provider(selected.Type), Bucket: selected.Bucket,
		Region: selected.Region, Endpoint: selected.Endpoint, Prefix: selected.Prefix,
		AccessKeyID: selected.AccessKeyID, SecretAccessKey: selected.SecretAccessKey,
	})
	require.NoError(t, err)

	site := "integration-" + uuid.NewString()
	setKey := path.Join("bqckup", site, time.Now().UTC().Format(storage.TimestampLayout))
	deleted := false
	t.Cleanup(func() {
		if !deleted { require.NoError(t, store.Delete(context.Background(), setKey)) }
	})
	contents := []byte("bqckup S3 integration probe")
	filename := filepath.Join(t.TempDir(), "probe.txt")
	require.NoError(t, os.WriteFile(filename, contents, 0o600))
	sum := sha256.Sum256(contents)
	artifact := storage.Artifact{Path: filename, Size: int64(len(contents)), SHA256: hex.EncodeToString(sum[:])}

	stored, err := store.Put(ctx, artifact, path.Join(setKey, "probe.txt"))
	require.NoError(t, err)
	assert.Equal(t, artifact.SHA256, stored.SHA256)
	sets, err := store.ListBackupSets(ctx, path.Join("bqckup", site))
	require.NoError(t, err)
	require.Len(t, sets, 1)
	require.NoError(t, store.Delete(ctx, setKey))
	deleted = true
}
```

- [ ] **Step 2: Prove the default suite remains offline**

Run: `go test ./internal/storage/s3compat -count=1`

Expected: PASS without reading integration selectors or contacting a provider.

- [ ] **Step 3: Run the opt-in disposable integration when a private config is available**

Run:

```bash
BQCKUP_S3_INTEGRATION_CONFIG=/tmp/bqckup-s3-integration/config \
BQCKUP_S3_INTEGRATION_STORAGE=testing \
go test -tags=integration ./internal/storage/s3compat -run TestDisposableS3CompatibleStorage -count=1 -v
```

Expected: PASS after one unique object is uploaded, verified, listed, and deleted. If no private config is supplied, report the integration test as not run; never copy credentials into the command, repository, or test source.

- [ ] **Step 4: Run full verification and inspect the complete diff**

Run:

```bash
make verify
sh scripts/check-docs.sh
git diff --check origin/main...HEAD
git status -sb
```

Expected: vet, race tests, build, docs, and whitespace checks pass; status contains only intentional plan checkbox changes if execution tracking was committed.

- [ ] **Step 5: Commit integration documentation if it changed after verification**

```bash
git add internal/storage/s3compat/integration_test.go docs/testing.md
git commit -m "test: add disposable S3 integration"
```

- [ ] **Step 6: Push and update draft PR #5**

```bash
git push
gh pr edit 5 --title "feat: add S3 and R2 storage"
gh pr checks 5 --watch
```

Expected: PR #5 contains the complete implementation, remains draft until maintainer review, and all configured checks pass.
