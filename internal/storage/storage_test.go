package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseBackupSetAcceptsCurrentAndLegacyLayouts(t *testing.T) {
	for _, value := range []string{"2026-08-20/00-09-30", "20-August-2026/00-09-30", "2026-08-20T00-09-30.123456789Z"} {
		t.Run(value, func(t *testing.T) {
			createdAt, err := ParseBackupSet(value)
			require.NoError(t, err)
			if value == "2026-08-20/00-09-30" || value == "20-August-2026/00-09-30" {
				assert.Equal(t, time.Date(2026, time.August, 20, 0, 9, 30, 0, time.UTC), createdAt)
			} else {
				assert.Equal(t, time.Date(2026, time.August, 20, 0, 9, 30, 123456789, time.UTC), createdAt)
			}
		})
	}
}

func TestParseBackupSetRejectsNonCanonicalNames(t *testing.T) {
	for _, value := range []string{
		"20-Agustus-2026/00-09-30.123456789Z",
		"20-August-2026",
		"20-August-2026/00:09:30",
		"20-august-2026/00-09-30",
	} {
		_, err := ParseBackupSet(value)
		require.Error(t, err, value)
	}
}

func TestFormatPackageKeyUsesLongMonthDateDirectory(t *testing.T) {
	createdAt := time.Date(2026, time.September, 16, 3, 4, 5, 0, time.UTC)
	assert.Equal(t, "16-September-2026/03-04-05-files.tar.gz", FormatPackageKey(createdAt, "files.tar.gz"))
}

func TestBackupSetForPackageAcceptsOldAndNewDateDirectories(t *testing.T) {
	for _, date := range []string{"2026-09-16", "16-September-2026"} {
		set, createdAt, err := BackupSetForPackage(date, "03-04-05-files.tar.gz")
		require.NoError(t, err, date)
		assert.Equal(t, date+"/03-04-05", set)
		assert.Equal(t, time.Date(2026, time.September, 16, 3, 4, 5, 0, time.UTC), createdAt)
	}
}
