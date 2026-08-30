// Package doctor implements the diagnostic checks behind bqckup doctor.
// It owns the check model and the check runner; the CLI only renders the
// report. Every check message is safe to print: no credentials, endpoints,
// or keys ever appear in them.
package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/process"
	"github.com/bqckup/bqckup-go/internal/storage"
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

// DatabaseProber verifies one database connection without writing anything.
type DatabaseProber interface {
	Probe(context.Context, config.DatabaseSource) error
}

// probeTimeout bounds every connectivity probe. Probes run sequentially, so
// one hung probe costs at most this long before the run continues.
var probeTimeout = 10 * time.Second

// Checker runs the doctor checks. Fields are populated by app.OpenDoctor.
type Checker struct {
	Cfg       *config.Config
	LoadErr   error
	Stores    map[string]storage.Store
	StoreErrs map[string]error
	DBProbers map[string]DatabaseProber
	Runner    process.ProcessRunner
}

// Run produces the full diagnostic report. It returns an error only for
// invalid --site input (unknown or disabled site); every other failure
// becomes a failing check so the whole picture is reported at once.
func (c *Checker) Run(ctx context.Context, siteFilter string) (DoctorReport, error) {
	checks := make([]DiagnosticCheck, 0, 8)
	passed := true

	addCheck := func(name, status, message string) {
		if status == "fail" {
			passed = false
		}
		checks = append(checks, DiagnosticCheck{Name: name, Status: status, Message: message})
	}

	if c.LoadErr != nil {
		addCheck("config", "fail", fmt.Sprintf("could not load configuration: %v", c.LoadErr))
		return DoctorReport{Checks: checks, Passed: false}, nil
	}
	cfg := c.Cfg
	addCheck("config", "ok", fmt.Sprintf("schema v%d valid (%d site(s), %d storage(s))", cfg.Version, len(cfg.Sites), len(cfg.Storages)))

	if siteFilter != "" {
		site, ok := cfg.Site(siteFilter)
		if !ok {
			return DoctorReport{}, fmt.Errorf("site %q is not configured", siteFilter)
		}
		if !site.Enabled {
			return DoctorReport{}, fmt.Errorf("site %q is disabled", siteFilter)
		}
	}

	// Directory checks, unchanged from the original doctor.
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

	// Per-site checks: incremental engine marker and password env vars,
	// then which database engines the selected enabled sites need.
	binaryMissing := map[string]bool{}
	needsMySQL := false
	needsPostgres := false

	for _, site := range sitesToCheck {
		if !site.Enabled {
			continue
		}
		if site.BackupMode == "incremental" {
			addCheck(fmt.Sprintf("engine:%s", site.Name), "ok", "built-in incremental engine")
			if val, ok := os.LookupEnv(site.Incremental.Password); !ok || val == "" {
				addCheck(fmt.Sprintf("secret:%s:%s", site.Name, site.Incremental.Password), "fail",
					fmt.Sprintf("password environment variable %q is not set or empty", site.Incremental.Password))
			} else {
				addCheck(fmt.Sprintf("secret:%s:%s", site.Name, site.Incremental.Password), "ok", "environment variable is set")
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
		if path, err := c.Runner.LookPath("mysqldump"); err != nil {
			addCheck("binary:mysqldump", "fail", "mysqldump executable not found in $PATH")
			binaryMissing["mysql"] = true
		} else {
			addCheck("binary:mysqldump", "ok", fmt.Sprintf("found at %s", path))
		}
	}

	if needsPostgres {
		if path, err := c.Runner.LookPath("pg_dump"); err != nil {
			addCheck("binary:pg_dump", "fail", "pg_dump executable not found in $PATH")
			binaryMissing["postgres"] = true
		} else {
			addCheck("binary:pg_dump", "ok", fmt.Sprintf("found at %s", path))
		}
	}

	// Connectivity probes, one per enabled database source of the selected
	// enabled sites, in config order. A missing dump binary skips the probe
	// instead of running it.
	engineBinary := map[string]string{"mysql": "mysqldump", "postgres": "pg_dump"}
	for _, site := range sitesToCheck {
		if !site.Enabled {
			continue
		}
		for _, db := range site.Sources.Databases {
			if !db.Enabled {
				continue
			}
			checkName := fmt.Sprintf("database:%s:%s", site.Name, db.Name)
			if binaryMissing[db.Engine] {
				addCheck(checkName, "skipped", engineBinary[db.Engine]+" not found")
				continue
			}
			prober, ok := c.DBProbers[db.Engine]
			if !ok {
				addCheck(checkName, "fail", "unsupported database engine")
				continue
			}
			probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
			probeErr := prober.Probe(probeCtx, db)
			cancel()
			if probeErr != nil {
				addCheck(checkName, "fail", probeErr.Error())
			} else {
				addCheck(checkName, "ok", "connection ok")
			}
		}
	}

	// Storage probes: every destination of the selected enabled sites,
	// deduplicated by name and sorted alphabetically. Unused storages get
	// no check.
	used := map[string]bool{}
	for _, site := range sitesToCheck {
		if !site.Enabled {
			continue
		}
		for _, destination := range site.Destinations {
			used[destination.Storage] = true
		}
	}
	storageNames := make([]string, 0, len(used))
	for name := range used {
		storageNames = append(storageNames, name)
	}
	sort.Strings(storageNames)
	for _, name := range storageNames {
		checkName := "storage:" + name
		if err, failed := c.StoreErrs[name]; failed {
			addCheck(checkName, "fail", err.Error())
			continue
		}
		store, ok := c.Stores[name]
		if !ok || store == nil {
			addCheck(checkName, "fail", "storage destination is unavailable")
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		probeErr := store.Probe(probeCtx)
		cancel()
		if probeErr != nil {
			addCheck(checkName, "fail", probeErr.Error())
		} else {
			addCheck(checkName, "ok", "access ok")
		}
	}

	return DoctorReport{Checks: checks, Passed: passed}, nil
}
