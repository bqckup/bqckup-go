package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

type rootDocument struct {
	Version       *int          `mapstructure:"version"`
	App           App           `mapstructure:"app"`
	Notifications Notifications `mapstructure:"notifications"`
}

type storageDocument struct {
	Storages map[string]Storage `mapstructure:"storages"`
}

type siteDocument struct {
	Version *int `mapstructure:"version"`
	Site    Site `mapstructure:"site"`
}

// Load reads a complete schema-v2 config tree from dir.
func Load(ctx context.Context, dir string) (Config, error) {
	if err := ctx.Err(); err != nil {
		return Config{}, err
	}

	dir, err := filepath.Abs(dir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve config directory: %w", err)
	}

	rootPath := filepath.Join(dir, "bqckup.yaml")
	var root rootDocument
	if err := decode(rootPath, &root, bindRootEnvironment); err != nil {
		return Config{}, err
	}

	storagePath, err := storagePath(dir)
	if err != nil {
		return Config{}, err
	}
	var stores storageDocument
	if err := decode(storagePath, &stores, nil); err != nil {
		return Config{}, err
	}
	for name, storage := range stores.Storages {
		if storage.Type == "r2" && storage.Region == "" && storage.Credentials.Source != "remote" {
			storage.Region = "auto"
			stores.Storages[name] = storage
		}
	}
	if err := validateCredentialFile(storagePath, stores.Storages); err != nil {
		return Config{}, err
	}

	sitePaths, err := filepath.Glob(filepath.Join(dir, "sites", "*.yaml"))
	if err != nil {
		return Config{}, &Error{File: filepath.Join(dir, "sites"), Kind: ErrorRead, Err: err}
	}
	sort.Strings(sitePaths)

	sites := make([]Site, 0, len(sitePaths))
	for _, sitePath := range sitePaths {
		if err := ctx.Err(); err != nil {
			return Config{}, err
		}
		var doc siteDocument
		if err := decode(sitePath, &doc, nil); err != nil {
			return Config{}, err
		}
		if err := validateDatabaseCredentialFile(sitePath, doc.Site); err != nil {
			return Config{}, err
		}
		doc.Site.SchemaVersion = versionOrDefault(doc.Version)
		doc.Site.SourceFile = sitePath
		if doc.Site.Policy.MinimumInterval == 0 {
			doc.Site.Policy.MinimumInterval = defaultMinimumInterval
		}
		if doc.Site.Policy.KeepLast == 0 {
			doc.Site.Policy.KeepLast = defaultKeepLast
		}
		sites = append(sites, doc.Site)
	}

	resolveRootPath := func(value string) string {
		if value == "" || filepath.IsAbs(value) {
			return filepath.Clean(value)
		}
		return filepath.Join(dir, value)
	}
	root.App.StateDatabase = resolveRootPath(root.App.StateDatabase)
	root.App.TemporaryDirectory = resolveRootPath(root.App.TemporaryDirectory)
	root.App.LockDirectory = resolveRootPath(root.App.LockDirectory)
	if root.App.LogLevel == "" {
		root.App.LogLevel = "info"
	}

	cfg := Config{
		Version:       versionOrDefault(root.Version),
		App:           root.App,
		Storages:      stores.Storages,
		Sites:         sites,
		Notifications: root.Notifications,
	}
	if primary, ok := cfg.PrimaryStorage(); ok {
		for index := range cfg.Sites {
			if cfg.Sites[index].Enabled && len(cfg.Sites[index].Destinations) == 0 {
				cfg.Sites[index].Destinations = []Destination{{Storage: primary}}
			}
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func versionOrDefault(version *int) int {
	if version == nil {
		return SchemaVersion
	}
	return *version
}

func validateDatabaseCredentialFile(path string, site Site) error {
	hasPassword := false
	for _, database := range site.Sources.Databases {
		if database.Password != "" {
			hasPassword = true
			break
		}
	}
	if !hasPassword {
		return nil
	}

	info, err := os.Lstat(path)
	if err != nil {
		return &Error{File: path, Kind: ErrorRead, Err: err}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return validationError(path, "site.sources.databases", "password-bearing site file must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return validationError(path, "site.sources.databases", "password-bearing site file must be a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return validationError(path, "site.sources.databases", "password-bearing site file must have mode 0600")
	}
	return nil
}

func storagePath(dir string) (string, error) {
	yamlPath := filepath.Join(dir, "config", "storages.yaml")
	ymlPath := filepath.Join(dir, "config", "storages.yml")
	_, yamlErr := os.Lstat(yamlPath)
	_, ymlErr := os.Lstat(ymlPath)
	if yamlErr == nil && ymlErr == nil {
		return "", &Error{
			File: filepath.Join(dir, "config"),
			Kind: ErrorRead,
			Err:  errors.New("both storages.yaml and storages.yml exist"),
		}
	}
	if yamlErr == nil {
		return yamlPath, nil
	}
	if ymlErr == nil {
		return ymlPath, nil
	}
	return yamlPath, nil
}

func validateCredentialFile(path string, storages map[string]Storage) error {
	hasCredentials := false
	for _, storage := range storages {
		if storage.AccessKeyID != "" || storage.SecretAccessKey != "" {
			hasCredentials = true
			break
		}
	}
	if !hasCredentials {
		return nil
	}

	info, err := os.Lstat(path)
	if err != nil {
		return &Error{File: path, Kind: ErrorRead, Err: err}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return validationError(path, "storages", "credential-bearing storage file must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return validationError(path, "storages", "credential-bearing storage file must be a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return validationError(path, "storages", "credential-bearing storage file must have mode 0600")
	}
	return nil
}

func decode(path string, target any, configure func(*viper.Viper)) error {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	if configure != nil {
		configure(v)
	}
	if err := v.ReadInConfig(); err != nil {
		return &Error{File: path, Kind: ErrorRead, Err: err}
	}
	decodeHook := mapstructure.ComposeDecodeHookFunc(
		mapstructure.StringToTimeDurationHookFunc(),
		mapstructure.StringToSliceHookFunc(","),
		legacyBooleanHook(),
	)
	if err := v.UnmarshalExact(target, viper.DecodeHook(decodeHook)); err != nil {
		return &Error{File: path, Kind: ErrorDecode, Err: err}
	}
	return nil
}

func legacyBooleanHook() mapstructure.DecodeHookFuncType {
	return func(from reflect.Type, to reflect.Type, value any) (any, error) {
		if from == nil || to == nil || from.Kind() != reflect.String || to.Kind() != reflect.Bool {
			return value, nil
		}
		switch strings.ToLower(reflect.ValueOf(value).String()) {
		case "yes":
			return true, nil
		case "no":
			return false, nil
		default:
			return value, nil
		}
	}
}

func bindRootEnvironment(v *viper.Viper) {
	v.SetEnvPrefix("BQCKUP")
	_ = v.BindEnv("app.state_database", "BQCKUP_STATE_DATABASE")
	_ = v.BindEnv("app.temporary_directory", "BQCKUP_TEMPORARY_DIRECTORY")
	_ = v.BindEnv("app.lock_directory", "BQCKUP_LOCK_DIRECTORY")
	_ = v.BindEnv("app.log_level", "BQCKUP_LOG_LEVEL")
}
