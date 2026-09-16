package retention

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/bqckup/bqckup-go/internal/storage"
)

type Store interface {
	ListBackupSets(ctx context.Context, sitePrefix string) ([]storage.BackupSet, error)
	Delete(ctx context.Context, key string) error
}

func Apply(ctx context.Context, store Store, sitePrefix string, keepLast int) error {
	if keepLast < 1 {
		return errors.New("retention keep_last must be at least 1")
	}
	sets, err := store.ListBackupSets(ctx, sitePrefix)
	if err != nil {
		return fmt.Errorf("list backup sets for retention: %w", err)
	}
	complete := sets[:0]
	for _, set := range sets {
		if set.Complete {
			complete = append(complete, set)
		}
	}
	sort.Slice(complete, func(i, j int) bool { return complete[i].CreatedAt.Before(complete[j].CreatedAt) })
	excess := len(complete) - keepLast
	for i := 0; i < excess; i++ {
		if err := store.Delete(ctx, complete[i].Key); err != nil {
			return fmt.Errorf("delete expired backup set %q: %w", complete[i].Key, err)
		}
	}
	return nil
}
