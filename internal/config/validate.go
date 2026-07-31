package config

import (
	"fmt"
	"path/filepath"
	"regexp"
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
	if c.StorageVersion != SchemaVersion {
		return validationError("config/storages.yaml", "version", "must equal %d", SchemaVersion)
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
	for name, storage := range c.Storages {
		field := "storages." + name
		if !safeName.MatchString(name) {
			return validationError("config/storages.yaml", field, "name contains unsupported characters")
		}
		if storage.Type != "local" {
			return validationError("config/storages.yaml", field+".type", "storage type %q is not available in this milestone", storage.Type)
		}
		if !filepath.IsAbs(storage.Directory) {
			return validationError("config/storages.yaml", field+".directory", "must be an absolute path")
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
