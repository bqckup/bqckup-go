package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/spf13/cobra"
)

type DiagnosticCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // "ok", "fail", "skipped"
	Message string `json:"message"`
}

type DoctorReport struct {
	Checks []DiagnosticCheck `json:"checks"`
	Passed bool              `json:"passed"`
}

func newDoctorCommand(opts *options) *cobra.Command {
	var siteFilter string
	command := &cobra.Command{
		Use:   "doctor",
		Short: "Preflight diagnostics and dependency checks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report := runDoctorChecks(cmd.Context(), opts.configDir, siteFilter)
			if opts.output == "json" {
				if err := writeJSON(cmd, report); err != nil {
					return err
				}
			} else {
				for _, check := range report.Checks {
					statusSymbol := "[✓]"
					if check.Status == "fail" {
						statusSymbol = "[✗]"
					} else if check.Status == "skipped" {
						statusSymbol = "[-]"
					}
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n", statusSymbol, check.Name, check.Message)
				}
			}

			if !report.Passed {
				return apperror.Wrap(apperror.CategoryPreflight, "doctor diagnostic checks failed", nil)
			}
			return nil
		},
	}
	command.Flags().StringVar(&siteFilter, "site", "", "filter diagnostic checks for a specific site")
	return command
}

func runDoctorChecks(ctx context.Context, configDir, siteFilter string) DoctorReport {
	checks := make([]DiagnosticCheck, 0, 8)
	passed := true

	addCheck := func(name, status, message string) {
		if status == "fail" {
			passed = false
		}
		checks = append(checks, DiagnosticCheck{Name: name, Status: status, Message: message})
	}

	cfg, err := config.Load(ctx, configDir)
	if err != nil {
		addCheck("config", "fail", fmt.Sprintf("could not load configuration: %v", err))
		return DoctorReport{Checks: checks, Passed: false}
	}
	addCheck("config", "ok", fmt.Sprintf("schema v%d valid (%d site(s), %d storage(s))", cfg.Version, len(cfg.Sites), len(cfg.Storages)))

	// Check directories
	for _, dir := range []struct {
		name string
		path string
	}{
		{"temp_dir", cfg.App.TemporaryDirectory},
		{"lock_dir", cfg.App.LockDirectory},
		{"state_db_dir", filepath.Dir(cfg.App.StateDatabase)},
	} {
		if dir.path != "" {
			if err := os.MkdirAll(dir.path, 0o700); err != nil {
				addCheck(dir.name, "fail", fmt.Sprintf("directory %s is not writable: %v", dir.path, err))
			} else {
				addCheck(dir.name, "ok", fmt.Sprintf("%s is writable", dir.path))
			}
		}
	}

	sitesToCheck := cfg.Sites
	if siteFilter != "" {
		filtered := make([]config.Site, 0, 1)
		for _, s := range cfg.Sites {
			if s.Name == siteFilter {
				filtered = append(filtered, s)
			}
		}
		sitesToCheck = filtered
	}

	needsMySQL := false
	needsPostgres := false
	needsRestic := false

	for _, site := range sitesToCheck {
		if !site.Enabled {
			continue
		}
		if site.BackupMode == "incremental" {
			if site.Incremental.Engine == "builtin" {
				// the builtin engine needs no restic binary; its storage
				// destinations must be local until L3 ships
				localOnly := true
				for _, destination := range site.Destinations {
					storageConfig, ok := cfg.Storages[destination.Storage]
					if !ok || storageConfig.Type != "local" {
						localOnly = false
						addCheck(fmt.Sprintf("engine:%s:destination:%s", site.Name, destination.Storage), "fail",
							"incremental engine 'builtin' requires local storage destinations")
					}
				}
				if localOnly {
					addCheck(fmt.Sprintf("engine:%s", site.Name), "ok", "builtin engine (no restic binary required)")
				}
			} else {
				needsRestic = true
			}
			if val, ok := os.LookupEnv(site.Incremental.PasswordEnv); !ok || val == "" {
				addCheck(fmt.Sprintf("secret:%s:%s", site.Name, site.Incremental.PasswordEnv), "fail",
					fmt.Sprintf("password environment variable %q is not set or empty", site.Incremental.PasswordEnv))
			} else {
				addCheck(fmt.Sprintf("secret:%s:%s", site.Name, site.Incremental.PasswordEnv), "ok", "environment variable is set")
			}
		}
		for _, db := range site.Sources.Databases {
			if !db.Enabled {
				continue
			}
			if db.Engine == "mysql" {
				needsMySQL = true
			} else if db.Engine == "postgres" {
				needsPostgres = true
			}
		}
	}

	if needsMySQL {
		if path, err := exec.LookPath("mysqldump"); err != nil {
			addCheck("binary:mysqldump", "fail", "mysqldump executable not found in $PATH")
		} else {
			addCheck("binary:mysqldump", "ok", fmt.Sprintf("found at %s", path))
		}
	}

	if needsPostgres {
		if path, err := exec.LookPath("pg_dump"); err != nil {
			addCheck("binary:pg_dump", "fail", "pg_dump executable not found in $PATH")
		} else {
			addCheck("binary:pg_dump", "ok", fmt.Sprintf("found at %s", path))
		}
	}

	if needsRestic {
		if path, err := exec.LookPath("restic"); err != nil {
			addCheck("binary:restic", "fail", "restic executable not found in $PATH")
		} else {
			addCheck("binary:restic", "ok", fmt.Sprintf("found at %s", path))
		}
	}

	return DoctorReport{Checks: checks, Passed: passed}
}
