package app

import (
	"context"
	"errors"

	databaseexporter "github.com/bqckup/bqckup-go/internal/backup/database"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/doctor"
	"github.com/bqckup/bqckup-go/internal/process"
	"github.com/bqckup/bqckup-go/internal/storage"
	localstorage "github.com/bqckup/bqckup-go/internal/storage/local"
	"github.com/bqckup/bqckup-go/internal/storage/s3compat"
)

// OpenDoctor builds the doctor checker for a configuration directory. A
// config load failure becomes a failing "config" check instead of a hard
// error so doctor always runs and reports. Store construction failures are
// recorded per storage and surface as failing checks; the history database
// is never opened.
func OpenDoctor(ctx context.Context, configDir string) (*doctor.Checker, error) {
	checker := &doctor.Checker{Runner: process.NewProcessRunner()}
	configuration, err := config.Load(ctx, configDir)
	if err != nil {
		checker.LoadErr = err
		return checker, nil
	}
	checker.Cfg = &configuration
	checker.Stores, checker.StoreErrs = buildDoctorStores(ctx, configuration.Storages)
	checker.DBProbers = buildDoctorProbers(configuration)
	return checker, nil
}

// buildDoctorStores constructs every configured store, recording failures
// per storage instead of aborting: doctor must report all problems at once.
func buildDoctorStores(ctx context.Context, configured map[string]config.Storage) (map[string]storage.Store, map[string]error) {
	stores := make(map[string]storage.Store, len(configured))
	storeErrs := make(map[string]error, len(configured))
	for name, value := range configured {
		var store storage.Store
		var err error
		switch value.Type {
		case "local":
			store, err = localstorage.New(value.Directory)
		case "s3", "r2":
			store, err = s3compat.New(ctx, s3compat.Options{
				Provider:        s3compat.Provider(value.Type),
				Bucket:          value.Bucket,
				Region:          value.Region,
				Endpoint:        value.Endpoint,
				Prefix:          value.Prefix,
				AccessKeyID:     value.AccessKeyID,
				SecretAccessKey: value.SecretAccessKey,
			})
		default:
			err = errors.New("unsupported storage type")
		}
		if err != nil {
			storeErrs[name] = err
			continue
		}
		stores[name] = store
	}
	return stores, storeErrs
}

// buildDoctorProbers constructs one probe per database engine enabled in
// any enabled site, without preflight: doctor must run when binaries are
// missing (the missing binary becomes a skipped check instead).
func buildDoctorProbers(configuration config.Config) map[string]doctor.DatabaseProber {
	probers := make(map[string]doctor.DatabaseProber)
	runner := process.NewProcessRunner()
	for _, site := range configuration.Sites {
		if !site.Enabled {
			continue
		}
		for _, source := range site.Sources.Databases {
			if !source.Enabled {
				continue
			}
			if _, exists := probers[source.Engine]; exists {
				continue
			}
			switch source.Engine {
			case "mysql":
				probers[source.Engine] = databaseexporter.NewMySQL(runner)
			case "postgres":
				probers[source.Engine] = databaseexporter.NewPostgres(runner)
			}
		}
	}
	return probers
}
