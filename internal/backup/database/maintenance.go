package database

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/process"
)

// TableCheck is one row returned by CHECK TABLE or REPAIR TABLE.
type TableCheck struct {
	Table       string `json:"table"`
	Engine      string `json:"engine,omitempty"`
	Operation   string `json:"operation"`
	MessageType string `json:"message_type"`
	Message     string `json:"message"`
	Healthy     bool   `json:"healthy"`
}

// CheckResult is the read-only health report for a database source.
type CheckResult struct {
	Healthy bool         `json:"healthy"`
	Tables  []TableCheck `json:"tables"`
}

// RepairResult is the result of one explicit table repair.
type RepairResult struct {
	Table       string `json:"table"`
	Engine      string `json:"engine"`
	Operation   string `json:"operation"`
	MessageType string `json:"message_type"`
	Message     string `json:"message"`
	Healthy     bool   `json:"healthy"`
}

// AutoRepairResult describes the checks and repairs performed after a dump
// reported a table-corruption error.
type AutoRepairResult struct {
	Checked  CheckResult    `json:"checked"`
	Repaired []RepairResult `json:"repaired"`
}

// AutoRepairer is the narrow interface used by a database exporter when a
// source explicitly opts into automatic repair.
type AutoRepairer interface {
	RepairCorruptTables(context.Context, config.DatabaseSource) (AutoRepairResult, error)
}

type databaseTable struct {
	Name   string
	Engine string
}

// ProcessMaintainer performs explicit MySQL/MariaDB table checks and repairs.
// Automatic callers must opt in through DatabaseSource.AutoRepair.
type ProcessMaintainer struct {
	process     process.ProcessRunner
	command     string
	passwordEnv string
}

func NewMySQLMaintainer(runner process.ProcessRunner) *ProcessMaintainer {
	return &ProcessMaintainer{process: runner, command: "mysql", passwordEnv: "MYSQL_PWD"}
}

func (m *ProcessMaintainer) Preflight() error {
	if _, err := m.process.LookPath(m.command); err != nil {
		return fmt.Errorf("required database maintenance client is unavailable: %w", err)
	}
	return nil
}

// Check lists the configured database tables and runs one read-only CHECK TABLE
// operation covering all of them.
func (m *ProcessMaintainer) Check(ctx context.Context, source config.DatabaseSource) (CheckResult, error) {
	if err := m.validateSource(ctx, source); err != nil {
		return CheckResult{}, err
	}
	tables, err := m.listTables(ctx, source)
	if err != nil {
		return CheckResult{}, err
	}
	result := CheckResult{Healthy: true, Tables: make([]TableCheck, 0, len(tables))}
	if len(tables) == 0 {
		return result, nil
	}

	identifiers := make([]string, 0, len(tables))
	for _, table := range tables {
		identifiers = append(identifiers, quoteIdentifier(table.Name))
	}
	var output bytes.Buffer
	if err := m.run(ctx, source, append(m.baseArguments(source), "-e", "CHECK TABLE "+strings.Join(identifiers, ",")), &output); err != nil {
		return CheckResult{}, err
	}
	for _, row := range parseTableRows(output.String()) {
		name, engine := matchTable(row.Table, tables)
		if name == "" {
			name = row.Table
		}
		result.Tables = append(result.Tables, TableCheck{
			Table: name, Engine: engine, Operation: row.Operation,
			MessageType: row.MessageType, Message: row.Message,
			Healthy: isHealthyTableMessage(row.MessageType, row.Message),
		})
	}
	if len(result.Tables) == 0 {
		return CheckResult{}, errors.New("mysql returned no CHECK TABLE results")
	}
	for _, table := range result.Tables {
		if !table.Healthy {
			result.Healthy = false
			break
		}
	}
	return result, nil
}

// Repair runs REPAIR TABLE for one exact table. MySQL's REPAIR TABLE is
// intentionally restricted to MyISAM and Aria; InnoDB must use recovery or a
// verified restore instead.
func (m *ProcessMaintainer) Repair(ctx context.Context, source config.DatabaseSource, tableName string) (RepairResult, error) {
	if err := m.validateSource(ctx, source); err != nil {
		return RepairResult{}, err
	}
	if strings.TrimSpace(tableName) == "" {
		return RepairResult{}, errors.New("table name is required")
	}
	tables, err := m.listTables(ctx, source)
	if err != nil {
		return RepairResult{}, err
	}
	var table databaseTable
	for _, candidate := range tables {
		if candidate.Name == tableName {
			table = candidate
			break
		}
	}
	if table.Name == "" {
		return RepairResult{}, fmt.Errorf("table %q was not found", tableName)
	}
	if !isRepairableEngine(table.Engine) {
		return RepairResult{}, fmt.Errorf("table %q uses %s; automatic REPAIR TABLE is unsupported, restore a verified dump or use database-native recovery", table.Name, table.Engine)
	}

	var output bytes.Buffer
	query := "REPAIR TABLE " + quoteIdentifier(table.Name)
	if err := m.run(ctx, source, append(m.baseArguments(source), "-e", query), &output); err != nil {
		return RepairResult{}, err
	}
	rows := parseTableRows(output.String())
	if len(rows) == 0 {
		return RepairResult{}, errors.New("mysql returned no REPAIR TABLE result")
	}
	row := rows[0]
	result := RepairResult{
		Table: table.Name, Engine: table.Engine, Operation: row.Operation,
		MessageType: row.MessageType, Message: row.Message,
		Healthy: isHealthyTableMessage(row.MessageType, row.Message),
	}
	if !result.Healthy {
		return result, fmt.Errorf("repair of table %q returned %s: %s", table.Name, row.MessageType, row.Message)
	}
	return result, nil
}

// RepairCorruptTables checks the database and repairs every unhealthy table
// only when all unhealthy tables use MyISAM or Aria. It refuses to perform a
// partial repair when an unhealthy InnoDB table is present.
func (m *ProcessMaintainer) RepairCorruptTables(ctx context.Context, source config.DatabaseSource) (AutoRepairResult, error) {
	checked, err := m.Check(ctx, source)
	if err != nil {
		return AutoRepairResult{}, err
	}
	result := AutoRepairResult{Checked: checked, Repaired: make([]RepairResult, 0)}
	if checked.Healthy {
		return result, nil
	}
	for _, table := range checked.Tables {
		if table.Healthy {
			continue
		}
		if !isRepairableEngine(table.Engine) {
			return result, fmt.Errorf("table %q uses %s; automatic REPAIR TABLE is unsupported, restore a verified dump or use database-native recovery", table.Table, table.Engine)
		}
	}
	for _, table := range checked.Tables {
		if table.Healthy {
			continue
		}
		repaired, err := m.Repair(ctx, source, table.Table)
		if err != nil {
			return result, err
		}
		result.Repaired = append(result.Repaired, repaired)
	}
	return result, nil
}

func (m *ProcessMaintainer) validateSource(ctx context.Context, source config.DatabaseSource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if source.Engine != "mysql" {
		return fmt.Errorf("database maintenance supports mysql sources only")
	}
	return m.Preflight()
}

func (m *ProcessMaintainer) listTables(ctx context.Context, source config.DatabaseSource) ([]databaseTable, error) {
	var output bytes.Buffer
	query := "SELECT TABLE_NAME, ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() ORDER BY TABLE_NAME"
	if err := m.run(ctx, source, append(m.baseArguments(source), "-e", query), &output); err != nil {
		return nil, err
	}
	lines := nonEmptyLines(output.String())
	tables := make([]databaseTable, 0, len(lines))
	for _, line := range lines {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 || parts[0] == "" {
			return nil, fmt.Errorf("mysql returned malformed table metadata")
		}
		tables = append(tables, databaseTable{Name: parts[0], Engine: parts[1]})
	}
	return tables, nil
}

func (m *ProcessMaintainer) baseArguments(source config.DatabaseSource) []string {
	return []string{
		"--no-defaults",
		"--host=" + source.Host,
		"--port=" + strconv.Itoa(source.Port),
		"--user=" + source.Username,
		"--database=" + source.Database,
		"--batch",
		"--skip-column-names",
		"--raw",
	}
}

func (m *ProcessMaintainer) run(ctx context.Context, source config.DatabaseSource, args []string, stdout io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var stderr bytes.Buffer
	if err := m.process.Run(ctx, process.ProcessSpec{
		Command: m.command,
		Args:    args,
		Env:     []string{m.passwordEnv + "=" + source.Password},
		Stdout:  stdout,
		Stderr:  &stderr,
	}); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if message := firstStderrLine(stderr.String()); message != "" {
			return errors.New(message)
		}
		return errors.New("database maintenance command failed")
	}
	return nil
}

type tableRow struct {
	Table       string
	Operation   string
	MessageType string
	Message     string
}

func parseTableRows(output string) []tableRow {
	rows := make([]tableRow, 0)
	for _, line := range nonEmptyLines(output) {
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) != 4 {
			continue
		}
		rows = append(rows, tableRow{Table: parts[0], Operation: parts[1], MessageType: parts[2], Message: parts[3]})
	}
	return rows
}

func nonEmptyLines(output string) []string {
	lines := make([]string, 0)
	for _, line := range strings.Split(output, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func matchTable(outputName string, tables []databaseTable) (string, string) {
	for _, table := range tables {
		if outputName == table.Name || strings.HasSuffix(outputName, "."+table.Name) {
			return table.Name, table.Engine
		}
	}
	return "", ""
}

func isHealthyTableMessage(messageType, message string) bool {
	return strings.EqualFold(strings.TrimSpace(messageType), "status") && strings.EqualFold(strings.TrimSpace(message), "OK")
}

func isRepairableEngine(engine string) bool {
	return strings.EqualFold(engine, "MyISAM") || strings.EqualFold(engine, "Aria")
}

func quoteIdentifier(identifier string) string {
	return "`" + strings.ReplaceAll(identifier, "`", "``") + "`"
}
