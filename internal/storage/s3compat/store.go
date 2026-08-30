package s3compat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	signerv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/ctxcopy"
	"github.com/bqckup/bqckup-go/internal/storage"
)

const (
	checksumMetadata = "bqckup-sha256"
	sizeMetadata     = "bqckup-size"
)

var ErrObjectExists = errors.New("storage object already exists")

type uploaderAPI interface {
	UploadObject(context.Context, *transfermanager.UploadObjectInput, ...func(*transfermanager.Options)) (*transfermanager.UploadObjectOutput, error)
}

type objectAPI interface {
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObjects(context.Context, *s3.DeleteObjectsInput, ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

type presignerAPI interface {
	PresignGetObject(context.Context, *s3.GetObjectInput, ...func(*s3.PresignOptions)) (*signerv4.PresignedHTTPRequest, error)
}

type Store struct {
	bucket    string
	prefix    string
	uploader  uploaderAPI
	client    objectAPI
	presigner presignerAPI
}

func newWithClients(options Options, uploader uploaderAPI, client objectAPI, presigner presignerAPI) *Store {
	return &Store{bucket: options.Bucket, prefix: options.Prefix, uploader: uploader, client: client, presigner: presigner}
}

func (s *Store) Put(ctx context.Context, pkg storage.Package, key string) (storage.StoredPackage, error) {
	if err := ctx.Err(); err != nil {
		return storage.StoredPackage{}, err
	}
	finalKey, err := storage.JoinPrefix(s.prefix, key)
	if err != nil {
		return storage.StoredPackage{}, err
	}
	checksum, size, err := inspectPackage(ctx, pkg)
	if err != nil {
		return storage.StoredPackage{}, err
	}

	file, err := os.Open(pkg.Path)
	if err != nil {
		return storage.StoredPackage{}, apperror.Hide("could not open backup package", err)
	}
	defer file.Close()
	_, err = s.uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(finalKey),
		Body:          file,
		IfNoneMatch:   aws.String("*"),
		MpuObjectSize: aws.Int64(size),
		Metadata: map[string]string{
			checksumMetadata: checksum,
			sizeMetadata:     strconv.FormatInt(size, 10),
		},
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return storage.StoredPackage{}, err
		}
		if isCollision(err) {
			return storage.StoredPackage{}, apperror.Hide(ErrObjectExists.Error(), ErrObjectExists)
		}
		return storage.StoredPackage{}, apperror.Hide("S3-compatible upload failed", err)
	}

	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(finalKey)})
	if err == nil {
		err = verifyRemote(head, size, checksum)
	}
	if err != nil {
		verificationErr := err
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			verificationErr = err
		} else {
			verificationErr = apperror.Hide("remote package verification failed", err)
		}
		_, cleanupErr := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(finalKey)})
		if cleanupErr != nil {
			verificationErr = apperror.Hide(verificationErr.Error(), errors.Join(verificationErr, cleanupErr))
		}
		return storage.StoredPackage{}, verificationErr
	}

	return storage.StoredPackage{Key: finalKey, Size: size, SHA256: checksum}, nil
}

func inspectPackage(ctx context.Context, pkg storage.Package) (string, int64, error) {
	if pkg.Size < 0 || len(pkg.SHA256) != sha256.Size*2 {
		return "", 0, errors.New("package size and SHA-256 are required")
	}
	if _, err := hex.DecodeString(pkg.SHA256); err != nil {
		return "", 0, errors.New("package SHA-256 is invalid")
	}
	file, err := os.Open(pkg.Path)
	if err != nil {
		return "", 0, apperror.Hide("could not inspect backup package", err)
	}
	defer file.Close()
	hash := sha256.New()
	size, err := ctxcopy.Copy(ctx, hash, file)
	if err != nil {
		return "", size, err
	}
	checksum := hex.EncodeToString(hash.Sum(nil))
	if size != pkg.Size || !strings.EqualFold(checksum, pkg.SHA256) {
		return "", size, errors.New("local package verification failed")
	}
	return checksum, size, nil
}

func verifyRemote(output *s3.HeadObjectOutput, size int64, checksum string) error {
	if output == nil || aws.ToInt64(output.ContentLength) != size {
		return errors.New("stored size does not match")
	}
	if !strings.EqualFold(metadataValue(output.Metadata, checksumMetadata), checksum) {
		return errors.New("stored checksum does not match")
	}
	if metadataValue(output.Metadata, sizeMetadata) != strconv.FormatInt(size, 10) {
		return errors.New("stored size metadata does not match")
	}
	return nil
}

func metadataValue(metadata map[string]string, name string) string {
	for key, value := range metadata {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func isCollision(err error) bool {
	var apiError smithy.APIError
	if errors.As(err, &apiError) && apiError.ErrorCode() == "PreconditionFailed" {
		return true
	}
	var statusError interface{ HTTPStatusCode() int }
	return errors.As(err, &statusError) && statusError.HTTPStatusCode() == 412
}

func isNotFound(err error) bool {
	var apiError smithy.APIError
	if errors.As(err, &apiError) && (apiError.ErrorCode() == "NotFound" || apiError.ErrorCode() == "NoSuchKey") {
		return true
	}
	var statusError interface{ HTTPStatusCode() int }
	return errors.As(err, &statusError) && statusError.HTTPStatusCode() == 404
}

// PresignLink checks that the object exists, then returns a temporary signed
// download URL for it. Key is relative to the storage document prefix. The
// only network call is the existence HEAD; presigning is client-side.
func (s *Store) PresignLink(ctx context.Context, key string, expires time.Duration) (storage.DownloadLink, error) {
	if err := ctx.Err(); err != nil {
		return storage.DownloadLink{}, err
	}
	finalKey, err := storage.JoinPrefix(s.prefix, key)
	if err != nil {
		return storage.DownloadLink{}, err
	}
	if _, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(finalKey)}); err != nil {
		if isNotFound(err) {
			return storage.DownloadLink{}, fmt.Errorf("object %q was not found in the destination", key)
		}
		return storage.DownloadLink{}, remoteOperationError("could not check the remote object", err)
	}
	if s.presigner == nil {
		return storage.DownloadLink{}, errors.New("presigning is unavailable")
	}
	request, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket:                     aws.String(s.bucket),
		Key:                        aws.String(finalKey),
		ResponseContentDisposition: aws.String("attachment; filename=" + path.Base(key)),
	}, func(options *s3.PresignOptions) {
		options.Expires = expires
	})
	if err != nil {
		return storage.DownloadLink{}, apperror.Hide("could not create the download link", err)
	}
	if request == nil {
		return storage.DownloadLink{}, errors.New("presigning produced no result")
	}
	return storage.DownloadLink{URL: request.URL, Key: key, ExpiresAt: time.Now().UTC().Add(expires)}, nil
}

var _ storage.Store = (*Store)(nil)

// Probe verifies read access to the destination with a single one-object
// list call under the storage prefix. Error text is sanitized: only the API
// error code surfaces, never provider messages, endpoints, or keys.
// ponytail: only ListObjectsV2 permission is verified; PutObject write
// access is not checked. Add a write probe if read-only credentials start
// masking backup failures.
func (s *Store) Probe(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(s.bucket),
		Prefix:  aws.String(s.prefix), // may be ""
		MaxKeys: aws.Int32(1),
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			return errors.New(apiErr.ErrorCode())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return errors.New("timed out")
		}
		return errors.New("request failed")
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, backupSetPrefix string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateBackupSetPrefix(backupSetPrefix); err != nil {
		return err
	}
	finalPrefix, err := storage.JoinPrefix(s.prefix, backupSetPrefix)
	if err != nil {
		return err
	}
	requestPrefix := finalPrefix + "/"
	var continuation *string
	for {
		output, listErr := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(requestPrefix),
			ContinuationToken: continuation,
		})
		if listErr != nil {
			return remoteOperationError("could not list remote backup objects", listErr)
		}
		if output == nil {
			return errors.New("remote object listing returned no result")
		}
		identifiers := make([]types.ObjectIdentifier, 0, len(output.Contents))
		for _, object := range output.Contents {
			key := aws.ToString(object.Key)
			if strings.HasPrefix(key, requestPrefix) {
				identifiers = append(identifiers, types.ObjectIdentifier{Key: aws.String(key)})
			}
		}
		for start := 0; start < len(identifiers); start += 1000 {
			end := start + 1000
			if end > len(identifiers) {
				end = len(identifiers)
			}
			deleted, deleteErr := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(s.bucket),
				Delete: &types.Delete{Objects: identifiers[start:end], Quiet: aws.Bool(true)},
			})
			if deleteErr != nil {
				return remoteOperationError("could not delete remote backup objects", deleteErr)
			}
			if deleted == nil || len(deleted.Errors) != 0 {
				return errors.New("remote backup deletion was incomplete")
			}
		}
		if !aws.ToBool(output.IsTruncated) {
			return nil
		}
		if output.NextContinuationToken == nil || aws.ToString(output.NextContinuationToken) == "" {
			return errors.New("remote object listing omitted its continuation token")
		}
		continuation = output.NextContinuationToken
	}
}

func (s *Store) ListBackupSets(ctx context.Context, sitePrefix string) ([]storage.BackupSet, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSitePrefix(sitePrefix); err != nil {
		return nil, err
	}
	finalPrefix, err := storage.JoinPrefix(s.prefix, sitePrefix)
	if err != nil {
		return nil, err
	}
	requestPrefix := finalPrefix + "/"
	setsByKey := make(map[string]storage.BackupSet)
	var continuation *string
	for {
		output, listErr := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(requestPrefix),
			ContinuationToken: continuation,
		})
		if listErr != nil {
			return nil, remoteOperationError("could not list remote backup sets", listErr)
		}
		if output == nil {
			return nil, errors.New("remote object listing returned no result")
		}
		for _, object := range output.Contents {
			key := aws.ToString(object.Key)
			if !strings.HasPrefix(key, requestPrefix) {
				continue
			}
			remainder := strings.TrimPrefix(key, requestPrefix)
			setName, createdAt, parseErr := parseBackupSetRemainder(remainder)
			if parseErr != nil {
				continue
			}
			setKey := path.Join(sitePrefix, setName)
			setsByKey[setKey] = storage.BackupSet{Key: setKey, CreatedAt: createdAt}
		}
		if !aws.ToBool(output.IsTruncated) {
			break
		}
		if output.NextContinuationToken == nil || aws.ToString(output.NextContinuationToken) == "" {
			return nil, errors.New("remote object listing omitted its continuation token")
		}
		continuation = output.NextContinuationToken
	}
	sets := make([]storage.BackupSet, 0, len(setsByKey))
	for _, set := range setsByKey {
		sets = append(sets, set)
	}
	sort.Slice(sets, func(left, right int) bool { return sets[left].CreatedAt.Before(sets[right].CreatedAt) })
	return sets, nil
}

func (s *Store) ListPackages(ctx context.Context, setPrefix string) ([]storage.RemotePackage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateBackupSetPrefix(setPrefix); err != nil {
		return nil, err
	}
	finalPrefix, err := storage.JoinPrefix(s.prefix, setPrefix)
	if err != nil {
		return nil, err
	}
	requestPrefix := finalPrefix + "/"
	var packages []storage.RemotePackage
	var continuation *string
	for {
		output, listErr := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(requestPrefix),
			ContinuationToken: continuation,
		})
		if listErr != nil {
			return nil, remoteOperationError("could not list remote backup packages", listErr)
		}
		if output == nil {
			return nil, errors.New("remote object listing returned no result")
		}
		for _, object := range output.Contents {
			key := aws.ToString(object.Key)
			if !strings.HasPrefix(key, requestPrefix) {
				continue
			}
			packages = append(packages, storage.RemotePackage{
				Key:       setPrefix + "/" + strings.TrimPrefix(key, requestPrefix),
				Size:      aws.ToInt64(object.Size),
				CreatedAt: aws.ToTime(object.LastModified),
			})
		}
		if !aws.ToBool(output.IsTruncated) {
			return packages, nil
		}
		if output.NextContinuationToken == nil || aws.ToString(output.NextContinuationToken) == "" {
			return nil, errors.New("remote object listing omitted its continuation token")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		continuation = output.NextContinuationToken
	}
}

func validateSitePrefix(sitePrefix string) error {
	if err := storage.ValidateKey(sitePrefix); err != nil {
		return err
	}
	parts := strings.Split(sitePrefix, "/")
	if (len(parts) != 2 || parts[0] != "bqckup" || !config.SafeName.MatchString(parts[1])) &&
		(len(parts) != 3 || parts[0] != "bqckup" || !config.SafeName.MatchString(parts[1]) || !config.SafeName.MatchString(parts[2])) {
		return errors.New("invalid backup site prefix")
	}
	return nil
}

func validateBackupSetPrefix(prefix string) error {
	if err := storage.ValidateKey(prefix); err != nil {
		return err
	}
	parts := strings.Split(prefix, "/")
	if (len(parts) != 3 && len(parts) != 4 && len(parts) != 5) || parts[0] != "bqckup" {
		return errors.New("invalid backup set prefix")
	}
	if len(parts) >= 4 && !config.SafeName.MatchString(parts[1]) {
		return errors.New("invalid backup set prefix")
	}
	if len(parts) == 5 && !config.SafeName.MatchString(parts[2]) {
		return errors.New("invalid backup set prefix")
	}
	setStart := 2
	if len(parts) == 5 {
		setStart = 3
	}
	if _, err := storage.ParseBackupSet(strings.Join(parts[setStart:], "/")); err != nil {
		return errors.New("invalid backup set prefix")
	}
	return nil
}

func parseBackupSetRemainder(remainder string) (string, time.Time, error) {
	parts := strings.Split(remainder, "/")
	if len(parts) >= 3 {
		setName := path.Join(parts[0], parts[1])
		if createdAt, err := storage.ParseBackupSet(setName); err == nil {
			return setName, createdAt, nil
		}
	}
	if len(parts) >= 2 {
		if createdAt, err := storage.ParseBackupSet(parts[0]); err == nil {
			return parts[0], createdAt, nil
		}
	}
	return "", time.Time{}, errors.New("invalid backup set object key")
}

func remoteOperationError(message string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return apperror.Hide(message, err)
}
