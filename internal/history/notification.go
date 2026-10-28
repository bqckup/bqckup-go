package history

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// NotificationState is delivery state, separate from factual backup history.
type NotificationState struct {
	Key         string `gorm:"primaryKey"`
	Fingerprint string
	LastSent    time.Time
}

func (r *Repository) LoadNotification(ctx context.Context, key string) (NotificationState, error) {
	state := NotificationState{Key: key}
	err := r.db.WithContext(ctx).Where("key = ?", key).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return NotificationState{Key: key}, nil
	}
	return state, err
}
func (r *Repository) SaveNotification(ctx context.Context, state NotificationState) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&state).Error
}
