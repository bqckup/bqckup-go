package history

import (
	"fmt"
	"os"
	"path/filepath"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Open creates an owner-only SQLite database configured for a single CLI writer.
func Open(path string) (*gorm.DB, func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, fmt.Errorf("create state directory: %w", err)
	}

	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000", filepath.ToSlash(path))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, nil, fmt.Errorf("open SQLite state database: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, nil, fmt.Errorf("secure SQLite state database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, nil, fmt.Errorf("access SQLite connection: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	closeDB := func() error {
		if err := sqlDB.Close(); err != nil {
			return fmt.Errorf("close SQLite state database: %w", err)
		}
		return nil
	}
	return db, closeDB, nil
}
