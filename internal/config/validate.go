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
)

const (
	defaultMinimumInterval = 24 * time.Hour
	defaultKeepLast        = 7
)

var safeName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

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

func validateStorage(name string, value Storage) error {
	field := "storages." + name
	if !safeName.MatchString(name) {
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
	return nil
}

func validateS3Storage(field string, value Storage) error {
	if value.Directory != "" {
		return validationError("config/storages.yaml", field+".directory", "directory is not valid for s3 storage")
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
	if !safeName.MatchString(site.Name) {
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
	if len(site.Sources.Files.Include) == 0 {
		return validationError(file, baseField+".sources.files.include", "at least one path is required")
	}
	for index, include := range site.Sources.Files.Include {
		if !filepath.IsAbs(include) {
			return validationError(file, fmt.Sprintf("%s.sources.files.include[%d]", baseField, index), "must be an absolute path")
		}
	}
	for index, exclude := range site.Sources.Files.Exclude {
		if !filepath.IsAbs(exclude) {
			return validationError(file, fmt.Sprintf("%s.sources.files.exclude[%d]", baseField, index), "must be an absolute path")
		}
	}
	for _, database := range site.Sources.Databases {
		if database.Enabled {
			return validationError(file, baseField+".sources.databases", "database exporters are not available in this milestone")
		}
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
