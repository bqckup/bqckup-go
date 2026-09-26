package app

import (
	"context"
	"testing"

	databaseexporter "github.com/bqckup/bqckup-go/internal/backup/database"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckDatabaseWiresConfiguredSource(t *testing.T) {
	maintainer := &fakeDatabaseMaintainer{checkResult: databaseexporter.CheckResult{Healthy: true}}
	application := databaseMaintenanceApp(maintainer)

	result, err := application.CheckDatabase(context.Background(), "eprints", "repository")

	require.NoError(t, err)
	assert.True(t, result.Healthy)
	assert.Equal(t, "RepositoryUntar347", maintainer.checked.Database)
}

func TestRepairDatabaseRequiresForce(t *testing.T) {
	maintainer := &fakeDatabaseMaintainer{}
	application := databaseMaintenanceApp(maintainer)

	_, err := application.RepairDatabase(context.Background(), "eprints", "repository", "legacy", false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--force")
	assert.Empty(t, maintainer.repairedTable)
}

func TestRepairDatabaseWiresForceOperation(t *testing.T) {
	maintainer := &fakeDatabaseMaintainer{repairResult: databaseexporter.RepairResult{Table: "legacy", Engine: "MyISAM", Healthy: true}}
	application := databaseMaintenanceApp(maintainer)

	result, err := application.RepairDatabase(context.Background(), "eprints", "repository", "legacy", true)

	require.NoError(t, err)
	assert.Equal(t, "legacy", result.Table)
	assert.Equal(t, "legacy", maintainer.repairedTable)
}

func TestCheckDatabaseRejectsUnknownSource(t *testing.T) {
	application := databaseMaintenanceApp(&fakeDatabaseMaintainer{})

	_, err := application.CheckDatabase(context.Background(), "eprints", "missing")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "database source \"missing\" was not found")
}

type fakeDatabaseMaintainer struct {
	checked       config.DatabaseSource
	repairedTable string
	checkResult   databaseexporter.CheckResult
	repairResult  databaseexporter.RepairResult
}

func (f *fakeDatabaseMaintainer) Preflight() error { return nil }

func (f *fakeDatabaseMaintainer) Check(_ context.Context, source config.DatabaseSource) (databaseexporter.CheckResult, error) {
	f.checked = source
	return f.checkResult, nil
}

func (f *fakeDatabaseMaintainer) Repair(_ context.Context, _ config.DatabaseSource, table string) (databaseexporter.RepairResult, error) {
	f.repairedTable = table
	return f.repairResult, nil
}

func databaseMaintenanceApp(maintainer databaseMaintainer) *App {
	return &App{
		configuration: config.Config{Sites: []config.Site{{
			Name: "eprints", Enabled: true,
			Sources: config.Sources{Databases: []config.DatabaseSource{{
				Name: "repository", Enabled: true, Engine: "mysql",
				Database: "RepositoryUntar347", Username: "backup", Password: "secret",
			}}},
		}}},
		databaseMaintainers: map[string]databaseMaintainer{"mysql": maintainer},
	}
}

var _ databaseMaintainer = (*fakeDatabaseMaintainer)(nil)
