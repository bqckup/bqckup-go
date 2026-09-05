package config

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bqckup/bqckup-go/internal/fileexclude"
)

const (
	defaultMinimumInterval = 24 * time.Hour
	defaultKeepLast        = 7
)

var (
	// SafeName matches names safe to use as site names, storage names,
	// and file-system path segments.
	SafeName     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	validEnvName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

func (c Config) Validate() error {
	if c.Version != SchemaVersion {
		return validationError("bqckup.yaml", "version", "must equal %d", SchemaVersion)
	}
	if c.App.StateDatabase == "" {
		return validationError("bqckup.yaml", "app.state_database", "is required")
	}
	if c.App.TemporaryDirectory == "" {
		return validationError("bqckup.yaml", "app.temporary_directory", "is required")
	}
	if c.App.LockDirectory == "" {
		return validationError("bqckup.yaml", "app.lock_directory", "is required")
	}
	if len(c.Storages) == 0 {
		return validationError("config/storages.yaml", "storages", "at least one storage is required")
	}
	names := make([]string, 0, len(c.Storages))
	primaryCount := 0
	for name, storage := range c.Storages {
		names = append(names, name)
		if storage.Primary {
			primaryCount++
		}
	}
	if primaryCount > 1 {
		return validationError("config/storages.yaml", "storages", "at most one storage may be primary")
	}
	sort.Strings(names)
	for _, name := range names {
		if err := validateStorage(name, c.Storages[name]); err != nil {
			return err
		}
	}

	seen := make(map[string]struct{}, len(c.Sites))
	for _, site := range c.Sites {
		if err := c.validateSite(site, seen); err != nil {
			return err
		}
	}
	return nil
}

// ValidateStorage validates one storage document. It is the exported
// single-entry form of validateStorage, used by callers that validate
// provider-resolved storages without re-validating the whole configuration.
func ValidateStorage(name string, value Storage) error {
	return validateStorage(name, value)
}

func validateStorage(name string, value Storage) error {
	field := "storages." + name
	if !SafeName.MatchString(name) {
		return validationError("config/storages.yaml", field, "name contains unsupported characters")
	}
	switch value.Type {
	case "local":
		return validateLocalStorage(field, value)
	case "s3":
		return validateS3Storage(field, value)
	case "r2":
		return validateR2Storage(field, value)
	default:
		return validationError("config/storages.yaml", field+".type", "type must be one of local, s3, or r2")
	}
}

func validateLocalStorage(field string, value Storage) error {
	if value.Directory == "" {
		return validationError("config/storages.yaml", field+".directory", "directory is required")
	}
	if !filepath.IsAbs(value.Directory) {
		return validationError("config/storages.yaml", field+".directory", "must be an absolute path")
	}
	remoteFields := []struct {
		name  string
		value string
	}{
		{"bucket", value.Bucket},
		{"access_key_id", value.AccessKeyID},
		{"secret_access_key", value.SecretAccessKey},
		{"region", value.Region},
		{"endpoint", value.Endpoint},
		{"prefix", value.Prefix},
	}
	for _, candidate := range remoteFields {
		if candidate.value != "" {
			return validationError("config/storages.yaml", field+"."+candidate.name, "%s is not valid for local storage", candidate.name)
		}
	}
	if value.Credentials.Source != "" || value.Credentials.URL != "" {
		return validationError("config/storages.yaml", field+".credentials", "credentials are not valid for local storage")
	}
	return nil
}

func validateS3Storage(field string, value Storage) error {
	if value.Directory != "" {
		return validationError("config/storages.yaml", field+".directory", "directory is not valid for s3 storage")
	}
	remote, err := validateCredentialSource(field, value)
	if err != nil {
		return err
	}
	if remote {
		return validateRemotePlaceholders(field, value)
	}
	if err := validateRemoteRequiredFields(field, value); err != nil {
		return err
	}
	if value.Region == "" {
		return validationError("config/storages.yaml", field+".region", "region is required")
	}
	if err := validateEndpoint(field+".endpoint", value.Endpoint, false); err != nil {
		return err
	}
	return validatePrefix(field+".prefix", value.Prefix)
}

func validateR2Storage(field string, value Storage) error {
	if value.Directory != "" {
		return validationError("config/storages.yaml", field+".directory", "directory is not valid for r2 storage")
	}
	remote, err := validateCredentialSource(field, value)
	if err != nil {
		return err
	}
	if remote {
		return validateRemotePlaceholders(field, value)
	}
	if err := validateRemoteRequiredFields(field, value); err != nil {
		return err
	}
	if value.Region != "auto" {
		return validationError("config/storages.yaml", field+".region", "region must be auto for r2 storage")
	}
	if err := validateEndpoint(field+".endpoint", value.Endpoint, true); err != nil {
		return err
	}
	parsed, _ := url.Parse(value.Endpoint)
	if !strings.EqualFold(parsed.Scheme, "https") {
		return validationError("config/storages.yaml", field+".endpoint", "endpoint must use HTTPS for r2 storage")
	}
	return validatePrefix(field+".prefix", value.Prefix)
}

func validateCredentialSource(field string, value Storage) (bool, error) {
	credentials := value.Credentials
	if credentials.Source == "" && credentials.URL == "" {
		return false, nil
	}
	if credentials.Source != "remote" {
		return false, validationError("config/storages.yaml", field+".credentials.source", "source must be remote")
	}
	if credentials.URL == "" {
		return false, validationError("config/storages.yaml", field+".credentials.url", "url is required")
	}
	if err := validateRemoteProviderURL(field+".credentials.url", credentials.URL); err != nil {
		return false, err
	}
	return true, nil
}

func validateRemoteProviderURL(field, raw string) error {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return validationError("config/storages.yaml", field, "must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return validationError("config/storages.yaml", field, "must not contain user information or a fragment")
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return validationError("config/storages.yaml", field, "must use HTTPS unless the host is loopback")
	}
	return nil
}

func validateRemotePlaceholders(field string, value Storage) error {
	fields := []struct {
		name  string
		value string
	}{
		{"bucket", value.Bucket},
		{"access_key_id", value.AccessKeyID},
		{"secret_access_key", value.SecretAccessKey},
		{"region", value.Region},
		{"endpoint", value.Endpoint},
	}
	for _, candidate := range fields {
		if candidate.value != "" {
			return validationError("config/storages.yaml", field+"."+candidate.name, "%s must not be set when credentials.source is remote", candidate.name)
		}
	}
	return validatePrefix(field+".prefix", value.Prefix)
}

func validateRemoteRequiredFields(field string, value Storage) error {
	required := []struct {
		name  string
		value string
	}{
		{"bucket", value.Bucket},
		{"access_key_id", value.AccessKeyID},
		{"secret_access_key", value.SecretAccessKey},
	}
	for _, candidate := range required {
		if strings.TrimSpace(candidate.value) == "" {
			return validationError("config/storages.yaml", field+"."+candidate.name, "%s is required", candidate.name)
		}
	}
	return nil
}

func validateEndpoint(field, raw string, required bool) error {
	if raw == "" {
		if required {
			return validationError("config/storages.yaml", field, "endpoint is required")
		}
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" {
		return validationError("config/storages.yaml", field, "endpoint must be an absolute URL")
	}
	if parsed.User != nil {
		return validationError("config/storages.yaml", field, "endpoint must not contain user information")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return validationError("config/storages.yaml", field, "endpoint must not contain a query or fragment")
	}
	if strings.EqualFold(parsed.Scheme, "https") {
		return nil
	}
	if strings.EqualFold(parsed.Scheme, "http") && isLoopbackHost(parsed.Hostname()) {
		return nil
	}
	return validationError("config/storages.yaml", field, "endpoint must use HTTPS except for loopback HTTP")
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validatePrefix(field, prefix string) error {
	if prefix == "" {
		return nil
	}
	if strings.Contains(prefix, `\`) || path.IsAbs(prefix) || path.Clean(prefix) != prefix {
		return validationError("config/storages.yaml", field, "prefix must be a safe relative object prefix")
	}
	for _, segment := range strings.Split(prefix, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return validationError("config/storages.yaml", field, "prefix must be a safe relative object prefix")
		}
	}
	return nil
}

func (c Config) validateSite(site Site, seen map[string]struct{}) error {
	file := site.SourceFile
	if file == "" {
		file = "sites/" + site.Name + ".yaml"
	}
	baseField := "sites." + site.Name
	if site.SchemaVersion != SchemaVersion {
		return validationError(file, "version", "must equal %d", SchemaVersion)
	}
	if !SafeName.MatchString(site.Name) {
		return validationError(file, baseField+".name", "contains unsupported characters")
	}
	if _, exists := seen[site.Name]; exists {
		return validationError(file, baseField+".name", "duplicate site name")
	}
	seen[site.Name] = struct{}{}

	filename := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	if filename != site.Name {
		return validationError(file, baseField+".name", "must match filename %q", filename)
	}
	if !site.Enabled {
		return nil
	}
	if site.BackupMode != "" && site.BackupMode != "full" && site.BackupMode != "incremental" {
		return validationError(file, baseField+".backup_mode", "must be 'full' or 'incremental'")
	}
	if site.BackupMode == "incremental" {
		if site.Incremental.PasswordEnv == "" {
			return validationError(file, baseField+".incremental.password_env", "is required")
		}
		if !validEnvName.MatchString(site.Incremental.PasswordEnv) {
			return validationError(file, baseField+".incremental.password_env", "must be a valid environment variable name")
		}
	}
	if len(site.Sources.Files.Include) == 0 {
		return validationError(file, baseField+".sources.files.include", "at least one path is required")
	}
	for index, include := range site.Sources.Files.Include {
		if !filepath.IsAbs(include) {
			return validationError(file, fmt.Sprintf("%s.sources.files.include[%d]", baseField, index), "must be an absolute path")
		}
	}
	for index, exclude := range site.Sources.Files.Exclude {
		if err := fileexclude.Validate(exclude); err != nil {
			return validationError(file, fmt.Sprintf("%s.sources.files.exclude[%d]", baseField, index), "invalid pattern: %v", err)
		}
	}
	databaseNames := make(map[string]struct{}, len(site.Sources.Databases))
	for index, database := range site.Sources.Databases {
		field := fmt.Sprintf("%s.sources.databases[%d]", baseField, index)
		if !database.Enabled {
			continue
		}
		if err := validateDatabaseSource(file, field, database); err != nil {
			return err
		}
		if _, exists := databaseNames[database.Name]; exists {
			return validationError(file, field+".name", "duplicate database source name")
		}
		databaseNames[database.Name] = struct{}{}
	}
	if len(site.Destinations) == 0 {
		return validationError(file, baseField+".destinations", "at least one destination is required")
	}
	for index, destination := range site.Destinations {
		if _, exists := c.Storages[destination.Storage]; !exists {
			return validationError(file, fmt.Sprintf("%s.destinations[%d].storage", baseField, index), "references unknown storage %q", destination.Storage)
		}
	}
	if site.Policy.MinimumInterval <= 0 {
		return validationError(file, baseField+".policy.minimum_interval", "must be positive")
	}
	if site.Policy.KeepLast < 1 {
		return validationError(file, baseField+".policy.keep_last", "must be at least 1")
	}
	return nil
}

func validateDatabaseSource(file, field string, source DatabaseSource) error {
	if !SafeName.MatchString(source.Name) {
		return validationError(file, field+".name", "must be a safe source name")
	}
	if source.Engine != "mysql" && source.Engine != "postgres" {
		return validationError(file, field+".engine", "must be mysql or postgres")
	}
	if strings.TrimSpace(source.Host) == "" {
		return validationError(file, field+".host", "is required")
	}
	if source.Port < 1 || source.Port > 65535 {
		return validationError(file, field+".port", "must be between 1 and 65535")
	}
	if strings.TrimSpace(source.Database) == "" {
		return validationError(file, field+".database", "is required")
	}
	if strings.TrimSpace(source.Username) == "" {
		return validationError(file, field+".username", "is required")
	}
	if source.Password == "" {
		return validationError(file, field+".password", "is required")
	}
	return nil
}
