package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatBackupSetUsesReadableEnglishUTCDateAndUniqueRunTime(t *testing.T) {
	createdAt := time.Date(2026, time.August, 20, 8, 9, 30, 123456789, time.FixedZone("WITA", 8*60*60))

	assert.Equal(t, "20-August-2026/00-09-30", FormatBackupSet(createdAt))
}

func TestParseBackupSetAcceptsCurrentAndLegacyLayouts(t *testing.T) {
	for _, value := range []string{
		"20-August-2026/00-09-30",
		"2026-08-20T00-09-30.123456789Z",
	} {
		t.Run(value, func(t *testing.T) {
			createdAt, err := ParseBackupSet(value)
			require.NoError(t, err)
			if value == "20-August-2026/00-09-30" {
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
