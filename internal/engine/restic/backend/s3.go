package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/engine/restic"
)

// S3Options configures an S3-compatible repository backend (S3, R2,
// MinIO). Credentials live in memory only; they never appear in output.
type S3Options struct {
	Bucket          string
	Endpoint        string
	Prefix          string // repository root object-key prefix inside the bucket
	Region          string
	AccessKeyID     string
	SecretAccessKey string
}

// uploaderAPI is the transfer-manager surface the backend needs.
type uploaderAPI interface {
	UploadObject(context.Context, *transfermanager.UploadObjectInput, ...func(*transfermanager.Options)) (*transfermanager.UploadObjectOutput, error)
}

// objectAPI is the S3 client surface the backend needs (fake-able in tests).
type objectAPI interface {
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// S3 stores repository files as objects under the restic default layout
// (same type→directory mapping as Layout). A single PutObject is the S3
// equivalent of the local backend's stage+rename: the object appears
// complete or not at all.
type S3 struct {
	bucket   string
	prefix   string
	layout   Layout
	uploader uploaderAPI
	client   objectAPI
}

// NewS3 builds the backend with the AWS SDK. No network request is made
// here; failures surface on the first operation.
func NewS3(ctx context.Context, options S3Options) (*S3, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.Bucket == "" {
		return nil, errors.New("s3 backend: bucket is required")
	}
	if options.Prefix == "" {
		return nil, errors.New("s3 backend: repository prefix is required")
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
		return nil, apperror.Hide("could not initialize S3-compatible repository storage", err)
	}
	client := s3.NewFromConfig(sdkConfig, func(clientOptions *s3.Options) {
		if options.Endpoint != "" {
			clientOptions.BaseEndpoint = aws.String(options.Endpoint)
			clientOptions.UsePathStyle = true
		}
	})
	return newS3WithClients(options, transfermanager.New(client), client), nil
}

func newS3WithClients(options S3Options, uploader uploaderAPI, client objectAPI) *S3 {
	return &S3{
		bucket:   options.Bucket,
		prefix:   options.Prefix,
		layout:   Layout{Dir: strings.TrimRight(options.Prefix, "/")},
		uploader: uploader,
		client:   client,
	}
}

// keyFor maps a handle to its object key using the shared default layout.
func (b *S3) keyFor(h restic.Handle) (string, error) {
	path, err := b.layout.Path(h)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(path), nil
}

// Save uploads rd to the handle's object key. The object is content
// addressed, so re-saving identical bytes is harmless (restic semantics).
func (b *S3) Save(ctx context.Context, h restic.Handle, rd io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := b.keyFor(h)
	if err != nil {
		return err
	}
	_, err = b.uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
		Body:   rd,
	})
	if err != nil {
		return remoteError("could not store repository object", err)
	}
	return nil
}

// Load streams the requested byte range through fn. length 0 reads to the
// end of the object; a missing object surfaces via IsNotExist.
func (b *S3) Load(ctx context.Context, h restic.Handle, length int, offset int64, fn func(rd io.Reader) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := b.keyFor(h)
	if err != nil {
		return err
	}
	rangeValue := fmt.Sprintf("bytes=%d-", offset)
	if length > 0 {
		rangeValue = fmt.Sprintf("bytes=%d-%d", offset, offset+int64(length)-1)
	}
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
		Range:  aws.String(rangeValue),
	})
	if err != nil {
		if b.IsNotExist(err) {
			return err // raw: callers branch on IsNotExist
		}
		return remoteError("could not read repository object", err)
	}
	defer out.Body.Close()
	return fn(out.Body)
}

// Stat returns object information, or an error for which IsNotExist is true.
func (b *S3) Stat(ctx context.Context, h restic.Handle) (FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return FileInfo{}, err
	}
	key, err := b.keyFor(h)
	if err != nil {
		return FileInfo{}, err
	}
	out, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if b.IsNotExist(err) {
			return FileInfo{}, err // raw: callers branch on IsNotExist
		}
		return FileInfo{}, remoteError("could not inspect repository object", err)
	}
	return FileInfo{Name: h.Name, Size: aws.ToInt64(out.ContentLength)}, nil
}

// List calls fn for every stored file of the given type. Data files live
// in 256 <xx> subprefixes; a single prefix listing covers them all.
func (b *S3) List(ctx context.Context, t restic.FileType, fn func(h restic.Handle, size int64) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var dir string
	switch t {
	case restic.ConfigFile:
		dir = ""
	case restic.DataFile:
		dir = "data"
	case restic.KeyFileType:
		dir = "keys"
	case restic.IndexFile:
		dir = "index"
	case restic.SnapshotFile:
		dir = "snapshots"
	case restic.LockFile:
		dir = "locks"
	default:
		return errors.New("backend: unknown file type")
	}
	listPrefix := b.prefix
	if dir != "" {
		listPrefix = b.prefix + "/" + dir
	}
	listPrefix += "/"

	var continuation *string
	for {
		out, err := b.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(b.bucket),
			Prefix:            aws.String(listPrefix),
			ContinuationToken: continuation,
		})
		if err != nil {
			return remoteError("could not list repository objects", err)
		}
		if out == nil {
			return errors.New("repository object listing returned no result")
		}
		for _, object := range out.Contents {
			if err := ctx.Err(); err != nil {
				return err
			}
			key := aws.ToString(object.Key)
			var name string
			if t == restic.DataFile {
				name = key[strings.LastIndexByte(key, '/')+1:] // last segment (data/<xx>/<hex>)
			} else {
				name = strings.TrimPrefix(key, listPrefix)
			}
			if name == "" || strings.Contains(name, "/") {
				continue
			}
			if err := fn(restic.Handle{Type: t, Name: name}, aws.ToInt64(object.Size)); err != nil {
				return err
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			return nil
		}
		if out.NextContinuationToken == nil || aws.ToString(out.NextContinuationToken) == "" {
			return errors.New("repository object listing omitted its continuation token")
		}
		continuation = out.NextContinuationToken
	}
}

// Remove deletes one object. S3 deletion is idempotent, so removing a
// missing object is not an error (matching the local backend).
func (b *S3) Remove(ctx context.Context, h restic.Handle) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := b.keyFor(h)
	if err != nil {
		return err
	}
	_, err = b.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return remoteError("could not remove repository object", err)
	}
	return nil
}

// IsNotExist reports whether err means "no such object" (404 responses and
// the NoSuchKey/NotFound API codes).
func (b *S3) IsNotExist(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return true
		}
	}
	var statusErr interface{ HTTPStatusCode() int }
	if errors.As(err, &statusErr) {
		return statusErr.HTTPStatusCode() == 404
	}
	return false
}

// remoteError hides provider details (endpoints, request IDs) while
// preserving cancellation semantics.
func remoteError(message string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return apperror.Hide(message, err)
}
