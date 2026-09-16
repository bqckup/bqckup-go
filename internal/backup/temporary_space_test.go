package backup

import (
	"math"
	"testing"
)

func TestEnsureTemporarySpaceRejectsInsufficientSpace(t *testing.T) {
	err := ensureTemporarySpace(t.TempDir(), math.MaxInt64)
	if err == nil {
		t.Fatal("ensureTemporarySpace() succeeded with an impossible requirement")
	}
}
