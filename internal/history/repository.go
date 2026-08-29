package history

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

func (r *Repository) CreateRun(ctx context.Context, run *BackupRun) error {
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	if err := r.db.WithContext(ctx).Create(run).Error; err != nil {
		return fmt.Errorf("create backup run: %w", err)
	}
	return nil
}

func (r *Repository) FinishRun(
	ctx context.Context,
	id string,
	status RunStatus,
	finished time.Time,
	errorCategory string,
	errorMessage string,
) error {
	var run BackupRun
	if err := r.db.WithContext(ctx).First(&run, "id = ?", id).Error; err != nil {
		return fmt.Errorf("load backup run %s: %w", id, err)
	}
	duration := finished.Sub(run.StartedAt).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	updates := map[string]any{
		"status":          status,
		"finished_at":     finished.UTC(),
		"duration_millis": duration,
		"error_category":  errorCategory,
		"error_message":   errorMessage,
	}
	if err := r.db.WithContext(ctx).Model(&BackupRun{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return fmt.Errorf("finish backup run %s: %w", id, err)
	}
	return nil
}

func (r *Repository) CreatePackage(ctx context.Context, pkg *Package) error {
	if pkg.ID == "" {
		pkg.ID = uuid.NewString()
	}
	if err := r.db.WithContext(ctx).Create(pkg).Error; err != nil {
		return fmt.Errorf("create backup package: %w", err)
	}
	return nil
}

func (r *Repository) LastSuccessful(ctx context.Context, site string, before time.Time) (*BackupRun, error) {
	var run BackupRun
	query := r.db.WithContext(ctx).
		Where("site_name = ? AND status IN (?, ?)", site, StatusSuccess, StatusNoChange)
	if !before.IsZero() {
		query = query.Where("started_at < ?", before.UTC())
	}
	err := query.Order("started_at DESC").First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load last successful run for %s: %w", site, err)
	}
	return &run, nil
}

// ConsecutiveWithoutSuccess counts the site's trailing non-success runs
// (failed, cancelled, running) that form a streak with gaps of at most 24 h
// between consecutive started_at values. startedAt is the current run's
// start time, the window anchor. The newest row counts unconditionally.
func (r *Repository) ConsecutiveWithoutSuccess(ctx context.Context, site string, startedAt time.Time) (int, error) {
	var runs []BackupRun
	err := r.db.WithContext(ctx).
		Where("site_name = ? AND started_at >= ?", site, startedAt.Add(-24*time.Hour)).
		Order("started_at DESC").
		Find(&runs).Error
	if err != nil {
		return 0, fmt.Errorf("load runs for streak %s: %w", site, err)
	}
	if len(runs) == 0 {
		return 0, nil
	}

	streak := 1
	newer := runs[0]
	for _, row := range runs[1:] {
		if row.Status == StatusSuccess || row.Status == StatusNoChange {
			break
		}
		if newer.StartedAt.Sub(row.StartedAt) > 24*time.Hour {
			break
		}
		streak++
		newer = row
	}
	return streak, nil
}

// RunPackages returns the stored packages of one run, in deterministic
// source order. Failed package rows are excluded: notification payloads
// aggregate only what actually reached a destination.
func (r *Repository) RunPackages(ctx context.Context, runID string) ([]Package, error) {
	var packages []Package
	err := r.db.WithContext(ctx).
		Where("run_id = ? AND status = ?", runID, PackageStored).
		Order("source_kind, source_name, destination").Find(&packages).Error
	if err != nil {
		return nil, fmt.Errorf("load packages for run %s: %w", runID, err)
	}
	return packages, nil
}

func (r *Repository) ListRuns(ctx context.Context, filter RunFilter) ([]BackupRun, error) {
	query := r.db.WithContext(ctx).Preload("Packages").Order("started_at DESC")
	if filter.Site != "" {
		query = query.Where("site_name = ?", filter.Site)
	}
	if filter.Limit > 0 {
		query = query.Limit(filter.Limit)
	}
	var runs []BackupRun
	if err := query.Find(&runs).Error; err != nil {
		return nil, fmt.Errorf("list backup runs: %w", err)
	}
	return runs, nil
}
