package history

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// Migrate brings the database schema up to date. Today that is a single
// idempotent AutoMigrate; add a recorded version table when migrations require it
// arrives.
func Migrate(ctx context.Context, db *gorm.DB) error {
	if err := db.WithContext(ctx).AutoMigrate(&BackupRun{}, &Package{}); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}
