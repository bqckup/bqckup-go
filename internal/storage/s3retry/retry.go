// Package s3retry defines the retry policy shared by every S3 upload path.
package s3retry

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
)

const maxAttempts = 10

// New returns the standard AWS retryer with enough attempts to tolerate
// short-lived S3 connection failures during long-running backups.
func New() aws.Retryer {
	return retry.NewStandard(func(options *retry.StandardOptions) {
		options.MaxAttempts = maxAttempts
	})
}
