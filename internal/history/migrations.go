package history

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

const currentSchemaVersion = 1

// Migrate applies ordered, recorded database migrations.
func Migrate(ctx context.Context, db *gorm.DB) error {
	if err := db.WithContext(ctx).AutoMigrate(&SchemaMigration{}); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}

	var count int64
	if err := db.WithContext(ctx).Model(&SchemaMigration{}).
		Where("version = ?", currentSchemaVersion).Count(&count).Error; err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if count == 1 {
		return nil
	}

	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(&BackupRun{}, &Artifact{}); err != nil {
			return fmt.Errorf("apply migration %d: %w", currentSchemaVersion, err)
		}
		migration := SchemaMigration{Version: currentSchemaVersion, AppliedAt: time.Now().UTC()}
		if err := tx.Create(&migration).Error; err != nil {
			return fmt.Errorf("record migration %d: %w", currentSchemaVersion, err)
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}
