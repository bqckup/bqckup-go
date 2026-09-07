package s3retry

import "testing"

func TestNewAllowsTenAttemptsForTransientS3Failures(t *testing.T) {
	if got, want := New().MaxAttempts(), 10; got != want {
		t.Fatalf("MaxAttempts() = %d, want %d", got, want)
	}
}
