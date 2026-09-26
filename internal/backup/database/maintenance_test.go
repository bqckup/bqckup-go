package database

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMaintainerCheckReportsCorruptTable(t *testing.T) {
	runner := &maintenanceProcessRunner{
		outputs: []string{
			"archive\tInnoDB\ncache\tMyISAM\n",
			"archive\tcheck\tstatus\tOK\ncache\tcheck\terror\tTable is marked as crashed\n",
		},
	}
	maintainer := NewMySQLMaintainer(runner)

	result, err := maintainer.Check(context.Background(), maintenanceSource())

	require.NoError(t, err)
	assert.False(t, result.Healthy)
	require.Len(t, result.Tables, 2)
	assert.Equal(t, TableCheck{Table: "archive", Engine: "InnoDB", Operation: "check", MessageType: "status", Message: "OK", Healthy: true}, result.Tables[0])
	assert.Equal(t, TableCheck{Table: "cache", Engine: "MyISAM", Operation: "check", MessageType: "error", Message: "Table is marked as crashed", Healthy: false}, result.Tables[1])
	require.Len(t, runner.specs, 2)
	assert.Contains(t, runner.specs[0].Args, "--database=RepositoryUntar347")
	assert.Contains(t, strings.Join(runner.specs[1].Args, " "), "CHECK TABLE `archive`,`cache`")
}

func TestMaintainerRepairMyISAMTable(t *testing.T) {
	runner := &maintenanceProcessRunner{
		outputs: []string{
			"legacy\tMyISAM\n",
			"legacy\trepair\tstatus\tOK\n",
		},
	}
	maintainer := NewMySQLMaintainer(runner)

	result, err := maintainer.Repair(context.Background(), maintenanceSource(), "legacy")

	require.NoError(t, err)
	assert.True(t, result.Healthy)
	assert.Equal(t, "legacy", result.Table)
	assert.Equal(t, "MyISAM", result.Engine)
	assert.Contains(t, strings.Join(runner.specs[1].Args, " "), "REPAIR TABLE `legacy`")
}

func TestMaintainerRepairRejectsInnoDB(t *testing.T) {
	runner := &maintenanceProcessRunner{outputs: []string{"eprints\tInnoDB\n"}}
	maintainer := NewMySQLMaintainer(runner)

	_, err := maintainer.Repair(context.Background(), maintenanceSource(), "eprints")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "InnoDB")
	assert.Len(t, runner.specs, 1)
}

func TestMaintainerRepairEscapesTableIdentifier(t *testing.T) {
	runner := &maintenanceProcessRunner{
		outputs: []string{
			"odd`name\tAria\n",
			"odd`name\trepair\tstatus\tOK\n",
		},
	}
	maintainer := NewMySQLMaintainer(runner)

	_, err := maintainer.Repair(context.Background(), maintenanceSource(), "odd`name")

	require.NoError(t, err)
	assert.Contains(t, strings.Join(runner.specs[1].Args, " "), "REPAIR TABLE `odd``name`")
}

func TestMaintainerRepairCorruptTablesRepairsOnlyRepairableTables(t *testing.T) {
	runner := &maintenanceProcessRunner{
		outputs: []string{
			"archive\tInnoDB\ncache\tMyISAM\n",
			"archive\tcheck\tstatus\tOK\ncache\tcheck\terror\tTable is marked as crashed\n",
			"archive\tInnoDB\ncache\tMyISAM\n",
			"cache\trepair\tstatus\tOK\n",
		},
	}
	maintainer := NewMySQLMaintainer(runner)

	result, err := maintainer.RepairCorruptTables(context.Background(), maintenanceSource())

	require.NoError(t, err)
	assert.False(t, result.Checked.Healthy)
	require.Len(t, result.Repaired, 1)
	assert.Equal(t, "cache", result.Repaired[0].Table)
	assert.Equal(t, "MyISAM", result.Repaired[0].Engine)
	assert.Len(t, runner.specs, 4)
}

func TestMaintainerRepairCorruptTablesRefusesInnoDBWithoutPartialRepair(t *testing.T) {
	runner := &maintenanceProcessRunner{
		outputs: []string{
			"eprints\tInnoDB\n",
			"eprints\tcheck\terror\tTable is marked as crashed\n",
		},
	}
	maintainer := NewMySQLMaintainer(runner)

	_, err := maintainer.RepairCorruptTables(context.Background(), maintenanceSource())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "InnoDB")
	assert.Len(t, runner.specs, 2)
}

type maintenanceProcessRunner struct {
	specs   []process.ProcessSpec
	outputs []string
	errors  []error
}

func (r *maintenanceProcessRunner) LookPath(command string) (string, error) {
	return command, nil
}

func (r *maintenanceProcessRunner) Run(_ context.Context, spec process.ProcessSpec) error {
	index := len(r.specs)
	r.specs = append(r.specs, spec)
	if index < len(r.outputs) {
		_, _ = io.WriteString(spec.Stdout, r.outputs[index])
	}
	if index < len(r.errors) {
		return r.errors[index]
	}
	return nil
}

func maintenanceSource() config.DatabaseSource {
	return config.DatabaseSource{
		Name: "RepositoryUntar347", Engine: "mysql", Host: "127.0.0.1", Port: 3306,
		Database: "RepositoryUntar347", Username: "backup", Password: "secret",
	}
}

var _ process.ProcessRunner = (*maintenanceProcessRunner)(nil)
