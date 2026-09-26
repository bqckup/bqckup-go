package app

import (
	"context"
	"fmt"

	"github.com/bqckup/bqckup-go/internal/apperror"
	databaseexporter "github.com/bqckup/bqckup-go/internal/backup/database"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/process"
)

type databaseMaintainer interface {
	Preflight() error
	Check(context.Context, config.DatabaseSource) (databaseexporter.CheckResult, error)
	Repair(context.Context, config.DatabaseSource, string) (databaseexporter.RepairResult, error)
}

func buildDatabaseMaintainers(configuration config.Config, runner process.ProcessRunner) map[string]databaseMaintainer {
	maintainers := make(map[string]databaseMaintainer)
	for _, site := range configuration.Sites {
		if !site.Enabled {
			continue
		}
		for _, source := range site.Sources.Databases {
			if !source.Enabled || source.Engine != "mysql" {
				continue
			}
			if _, exists := maintainers[source.Engine]; !exists {
				maintainers[source.Engine] = databaseexporter.NewMySQLMaintainer(runner)
			}
		}
	}
	return maintainers
}

func (a *App) CheckDatabase(ctx context.Context, siteName, sourceName string) (databaseexporter.CheckResult, error) {
	source, maintainer, err := a.databaseSource(siteName, sourceName)
	if err != nil {
		return databaseexporter.CheckResult{}, err
	}
	if err := maintainer.Preflight(); err != nil {
		return databaseexporter.CheckResult{}, apperror.Wrap(apperror.CategoryPreflight, "database maintenance client is unavailable", err)
	}
	result, err := maintainer.Check(ctx, source)
	if err != nil {
		return databaseexporter.CheckResult{}, apperror.Wrap(apperror.CategoryExecution, "could not check database tables", err)
	}
	return result, nil
}

func (a *App) RepairDatabase(ctx context.Context, siteName, sourceName, tableName string, force bool) (databaseexporter.RepairResult, error) {
	if !force {
		return databaseexporter.RepairResult{}, apperror.Wrap(apperror.CategoryPreflight, "database repair requires --force", nil)
	}
	source, maintainer, err := a.databaseSource(siteName, sourceName)
	if err != nil {
		return databaseexporter.RepairResult{}, err
	}
	if err := maintainer.Preflight(); err != nil {
		return databaseexporter.RepairResult{}, apperror.Wrap(apperror.CategoryPreflight, "database maintenance client is unavailable", err)
	}
	result, err := maintainer.Repair(ctx, source, tableName)
	if err != nil {
		return result, apperror.Wrap(apperror.CategoryExecution, "could not repair database table", err)
	}
	return result, nil
}

func (a *App) databaseSource(siteName, sourceName string) (config.DatabaseSource, databaseMaintainer, error) {
	site, ok := a.configuration.Site(siteName)
	if !ok {
		return config.DatabaseSource{}, nil, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q was not found", siteName), nil)
	}
	if !site.Enabled {
		return config.DatabaseSource{}, nil, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("site %q is disabled", siteName), nil)
	}
	for _, source := range site.Sources.Databases {
		if source.Name != sourceName {
			continue
		}
		if !source.Enabled {
			return config.DatabaseSource{}, nil, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("database source %q is disabled", sourceName), nil)
		}
		maintainer, ok := a.databaseMaintainers[source.Engine]
		if !ok || maintainer == nil {
			return config.DatabaseSource{}, nil, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("database maintenance does not support engine %q", source.Engine), nil)
		}
		return source, maintainer, nil
	}
	return config.DatabaseSource{}, nil, apperror.Wrap(apperror.CategoryConfig, fmt.Sprintf("database source %q was not found", sourceName), nil)
}
