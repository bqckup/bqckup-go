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

func (r *Repository) CreateArtifact(ctx context.Context, artifact *Artifact) error {
	if artifact.ID == "" {
		artifact.ID = uuid.NewString()
	}
	if err := r.db.WithContext(ctx).Create(artifact).Error; err != nil {
		return fmt.Errorf("create backup artifact: %w", err)
	}
	return nil
}

func (r *Repository) LastSuccessful(ctx context.Context, site string) (*BackupRun, error) {
	var run BackupRun
	err := r.db.WithContext(ctx).
		Where("site_name = ? AND status = ?", site, StatusSuccess).
		Order("started_at DESC").First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load last successful run for %s: %w", site, err)
	}
	return &run, nil
}

// RunArtifacts returns the stored artifacts of one run, in deterministic
// source order. Failed artifact rows are excluded: notification payloads
// aggregate only what actually reached a destination.
func (r *Repository) RunArtifacts(ctx context.Context, runID string) ([]Artifact, error) {
	var artifacts []Artifact
	err := r.db.WithContext(ctx).
		Where("run_id = ? AND status = ?", runID, ArtifactStored).
		Order("source_kind, source_name, destination").Find(&artifacts).Error
	if err != nil {
		return nil, fmt.Errorf("load artifacts for run %s: %w", runID, err)
	}
	return artifacts, nil
}

func (r *Repository) ListRuns(ctx context.Context, filter RunFilter) ([]BackupRun, error) {
	query := r.db.WithContext(ctx).Preload("Artifacts").Order("started_at DESC")
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
