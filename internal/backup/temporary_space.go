package backup

import (
	"fmt"
	"math"

	"golang.org/x/sys/unix"
)

const temporarySpaceMinimumReserve uint64 = 64 << 20

// ensureTemporarySpace rejects a known archive/dump estimate when the local
// temporary filesystem cannot hold it plus a small safety margin. Estimates
// are deliberately optional: a source that cannot be scanned must retain the
// existing streaming backup behavior rather than become unbackupable.
func ensureTemporarySpace(directory string, estimate int64) error {
	if estimate <= 0 {
		return nil
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(directory, &stat); err != nil {
		return fmt.Errorf("inspect temporary directory free space: %w", err)
	}
	required := uint64(estimate)
	reserve := required / 10
	if reserve < temporarySpaceMinimumReserve {
		reserve = temporarySpaceMinimumReserve
	}
	if required > math.MaxUint64-reserve {
		required = math.MaxUint64
	} else {
		required += reserve
	}
	available := stat.Bavail * uint64(stat.Bsize)
	if available < required {
		return fmt.Errorf("temporary directory has %d bytes free; at least %d bytes are required", available, required)
	}
	return nil
}
