package s3compat

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bqckup/bqckup-go/internal/apperror"
)

type Provider string

const (
	ProviderS3 Provider = "s3"
	ProviderR2 Provider = "r2"
)

type Options struct {
	Provider        Provider
	Bucket          string
	Region          string
	Endpoint        string
	Prefix          string
	AccessKeyID     string
	SecretAccessKey string
}

// New constructs an S3-compatible store without making a network request.
func New(ctx context.Context, options Options) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.Provider != ProviderS3 && options.Provider != ProviderR2 {
		return nil, errors.New("unsupported S3-compatible provider")
	}
	if options.Provider == ProviderR2 && options.Region == "" {
		options.Region = "auto"
	}

	sdkConfig, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(options.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			options.AccessKeyID,
			options.SecretAccessKey,
			"",
		)),
		awsconfig.WithRetryer(func() aws.Retryer {
			return retry.NewStandard(func(retryOptions *retry.StandardOptions) {
				retryOptions.MaxAttempts = 3
			})
		}),
	)
	if err != nil {
		return nil, apperror.Hide("could not initialize S3-compatible storage", err)
	}

	client := s3.NewFromConfig(sdkConfig, func(clientOptions *s3.Options) {
		if options.Endpoint != "" {
			clientOptions.BaseEndpoint = aws.String(options.Endpoint)
			clientOptions.UsePathStyle = true
		}
	})
	uploader := transfermanager.New(client)
	return newWithClients(options, uploader, client), nil
}
