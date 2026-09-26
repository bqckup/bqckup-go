package cli

import (
	"fmt"
	"io"

	"github.com/bqckup/bqckup-go/internal/app"
	databaseexporter "github.com/bqckup/bqckup-go/internal/backup/database"
	"github.com/spf13/cobra"
)

func newDatabaseCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "database", Short: "Check or repair configured source databases"}

	var source string
	check := &cobra.Command{
		Use:   "check <site>",
		Short: "Check every table in one configured MySQL source",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError(cmd, "database check requires exactly one site")
			}
			return nil
		},
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if source == "" {
				return usageError(cmd, "--source is required")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApplication(cmd, opts.configDir, func(application *app.App) error {
				result, err := application.CheckDatabase(cmd.Context(), args[0], source)
				if err != nil {
					return err
				}
				if opts.output == "json" {
					return writeJSON(cmd, databaseCheckEnvelope{Site: args[0], Source: source, CheckResult: result})
				}
				if err := writeDatabaseCheckText(cmd.OutOrStdout(), args[0], source, result); err != nil {
					return err
				}
				if !result.Healthy {
					return errCheckProblems
				}
				return nil
			})
		},
	}
	check.Flags().StringVar(&source, "source", "", "configured database source name (required)")
	command.AddCommand(check)

	var repairSource, table string
	var force bool
	repair := &cobra.Command{
		Use:   "repair <site>",
		Short: "Repair one MyISAM or Aria table explicitly",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError(cmd, "database repair requires exactly one site")
			}
			return nil
		},
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if repairSource == "" {
				return usageError(cmd, "--source is required")
			}
			if table == "" {
				return usageError(cmd, "--table is required")
			}
			if !force {
				return usageError(cmd, "--force is required because repair changes the database")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApplication(cmd, opts.configDir, func(application *app.App) error {
				result, err := application.RepairDatabase(cmd.Context(), args[0], repairSource, table, force)
				if err != nil {
					return err
				}
				if opts.output == "json" {
					return writeJSON(cmd, databaseRepairEnvelope{Site: args[0], Source: repairSource, Status: "repaired", RepairResult: result})
				}
				return writeDatabaseRepairText(cmd.OutOrStdout(), args[0], repairSource, result)
			})
		},
	}
	repair.Flags().StringVar(&repairSource, "source", "", "configured database source name (required)")
	repair.Flags().StringVar(&table, "table", "", "exact table name to repair (required)")
	repair.Flags().BoolVar(&force, "force", false, "confirm the repair may change database contents (required)")
	command.AddCommand(repair)
	return command
}

type databaseCheckEnvelope struct {
	Site   string `json:"site"`
	Source string `json:"source"`
	databaseexporter.CheckResult
}

type databaseRepairEnvelope struct {
	Site   string `json:"site"`
	Source string `json:"source"`
	Status string `json:"status"`
	databaseexporter.RepairResult
}

func writeDatabaseCheckText(output io.Writer, site, source string, result databaseexporter.CheckResult) error {
	if result.Healthy {
		_, err := fmt.Fprintf(output, "database check %s/%s: healthy (%d tables)\n", site, source, len(result.Tables))
		return err
	}
	if _, err := fmt.Fprintf(output, "database check %s/%s: problems found\n", site, source); err != nil {
		return err
	}
	for _, table := range result.Tables {
		if table.Healthy {
			continue
		}
		if _, err := fmt.Fprintf(output, "%s %s %s: %s\n", table.Table, table.Engine, table.MessageType, table.Message); err != nil {
			return err
		}
	}
	return nil
}

func writeDatabaseRepairText(output io.Writer, site, source string, result databaseexporter.RepairResult) error {
	_, err := fmt.Fprintf(output, "database repair %s/%s/%s: %s\n", site, source, result.Table, result.Message)
	return err
}
