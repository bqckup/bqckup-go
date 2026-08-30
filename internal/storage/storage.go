package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// BackupDateLayout is the sortable UTC date directory.
	BackupDateLayout = "2006-01-02"
	// BackupRunLayout is the compact UTC time prefix used in package names.
	BackupRunLayout = "15-04-05"
	// TimestampLayout is the logical run prefix used by retention.
	TimestampLayout = BackupDateLayout + "/" + BackupRunLayout

	ReadableSecondsLayout = BackupDateLayout + "/" + BackupRunLayout
)

// FormatBackupSet returns the canonical directory path for a new archive set.
func FormatBackupSet(createdAt time.Time) string {
	return createdAt.UTC().Format(TimestampLayout)
}

// FormatPackageKey returns the date-relative object name for a full backup
// package. All packages from one run share the same time prefix.
func FormatPackageKey(createdAt time.Time, packageName string, runID ...string) string {
	name := createdAt.UTC().Format(BackupRunLayout)
	if len(runID) > 0 && runID[0] != "" {
		compact := strings.ReplaceAll(runID[0], "-", "")
		if len(compact) > 8 {
			compact = compact[:8]
		}
		name += "-" + compact
	}
	return createdAt.UTC().Format(BackupDateLayout) + "/" + name + "-" + packageName
}

// ParseBackupSet parses the logical date/time run prefix.
func ParseBackupSet(value string) (time.Time, error) {
	parts := strings.Split(value, "/")
	if len(parts) == 2 && len(parts[1]) >= 8 {
		if createdAt, err := time.Parse(TimestampLayout, parts[0]+"/"+parts[1][:8]); err == nil && (len(parts[1]) == 8 || parts[1][8] == '-') {
			return createdAt, nil
		}
	}
	for _, layout := range []string{TimestampLayout, "02-January-2006/15-04-05.000000000", "02-January-2006/15-04-05", "2006-01-02T15-04-05.000000000Z"} {
		createdAt, err := time.Parse(layout, value)
		if err == nil && createdAt.Format(layout) == value {
			return createdAt, nil
		}
	}
	return time.Time{}, errors.New("invalid backup set timestamp")
}

func IsFlatBackupSet(value string) bool {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || len(parts[1]) < 8 {
		return false
	}
	_, err := ParseBackupSet(value)
	return err == nil
}

// ParseBackupPackage parses a new flat-date package name and returns its run
// timestamp. The package suffix is intentionally not restricted so database
// names remain configurable.
func ParseBackupPackage(value string) (time.Time, error) {
	// The time prefix is exactly HH-mm-ss, followed by a separating dash.
	if len(value) < len("00-00-00-") || value[8] != '-' {
		return time.Time{}, errors.New("invalid backup package name")
	}
	if value[8] != '-' || len(value) <= 9 {
		return time.Time{}, errors.New("invalid backup package name")
	}
	return time.Parse(BackupRunLayout, value[:8])
}

// BackupSetForPackage returns the logical set prefix for a new package.
func BackupSetForPackage(date, packageName string) (string, time.Time, error) {
	if _, err := ParseBackupPackage(packageName); err != nil {
		return "", time.Time{}, err
	}
	dateTime, err := time.Parse(BackupDateLayout+"/"+BackupRunLayout, date+"/"+packageName[:8])
	if err != nil {
		return "", time.Time{}, fmt.Errorf("invalid backup date: %w", err)
	}
	setTime := packageName[:8]
	if len(packageName) > 9 && packageName[8] == '-' {
		if separator := strings.IndexByte(packageName[9:], '-'); separator >= 0 {
			setTime = packageName[:9+separator]
		}
	}
	return date + "/" + setTime, dateTime, nil
}

type StoredPackage struct {
	Key    string
	Size   int64
	SHA256 string
}

// Package is the verified local file handed to a storage adapter.
type Package struct {
	Path   string
	Size   int64
	SHA256 string
}

type BackupSet struct {
	Key       string
	CreatedAt time.Time
}

// DownloadLink is a temporary signed URL for one stored object. Key is
// relative to the storage document prefix, exactly as storage list prints it.
type DownloadLink struct {
	URL       string
	Key       string
	ExpiresAt time.Time
}

// RemotePackage is one stored object listed from a remote destination.
// Key is relative to the storage document prefix (bqckup/<site>/<set>/<name>).
type RemotePackage struct {
	Key       string
	Size      int64
	CreatedAt time.Time
}

type Store interface {
	Put(ctx context.Context, pkg Package, key string) (StoredPackage, error)
	Delete(ctx context.Context, key string) error
	ListBackupSets(ctx context.Context, sitePrefix string) ([]BackupSet, error)
	Probe(ctx context.Context) error
}
