package s3compat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
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
}

type Store struct {
	bucket   string
	prefix   string
	uploader uploaderAPI
	client   objectAPI
}

func newWithClients(options Options, uploader uploaderAPI, client objectAPI) *Store {
	return &Store{bucket: options.Bucket, prefix: options.Prefix, uploader: uploader, client: client}
}

func (s *Store) Put(ctx context.Context, artifact storage.Artifact, key string) (storage.StoredArtifact, error) {
	if err := ctx.Err(); err != nil {
		return storage.StoredArtifact{}, err
	}
	finalKey, err := storage.JoinPrefix(s.prefix, key)
	if err != nil {
		return storage.StoredArtifact{}, err
	}
	checksum, size, err := inspectArtifact(ctx, artifact)
	if err != nil {
		return storage.StoredArtifact{}, err
	}

	file, err := os.Open(artifact.Path)
	if err != nil {
		return storage.StoredArtifact{}, hiddenError("could not open backup artifact", err)
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
			return storage.StoredArtifact{}, err
		}
		if isCollision(err) {
			return storage.StoredArtifact{}, hiddenError(ErrObjectExists.Error(), ErrObjectExists)
		}
		return storage.StoredArtifact{}, hiddenError("S3-compatible upload failed", err)
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
			verificationErr = hiddenError("remote artifact verification failed", err)
		}
		_, cleanupErr := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(finalKey)})
		if cleanupErr != nil {
			verificationErr = hiddenError(verificationErr.Error(), errors.Join(verificationErr, cleanupErr))
		}
		return storage.StoredArtifact{}, verificationErr
	}

	return storage.StoredArtifact{Key: finalKey, Size: size, SHA256: checksum}, nil
}

func inspectArtifact(ctx context.Context, artifact storage.Artifact) (string, int64, error) {
	if artifact.Size < 0 || len(artifact.SHA256) != sha256.Size*2 {
		return "", 0, errors.New("artifact size and SHA-256 are required")
	}
	if _, err := hex.DecodeString(artifact.SHA256); err != nil {
		return "", 0, errors.New("artifact SHA-256 is invalid")
	}
	file, err := os.Open(artifact.Path)
	if err != nil {
		return "", 0, hiddenError("could not inspect backup artifact", err)
	}
	defer file.Close()
	hash := sha256.New()
	size, err := copyWithContext(ctx, hash, file)
	if err != nil {
		return "", size, err
	}
	checksum := hex.EncodeToString(hash.Sum(nil))
	if size != artifact.Size || !strings.EqualFold(checksum, artifact.SHA256) {
		return "", size, errors.New("local artifact verification failed")
	}
	return checksum, size, nil
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 128*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			count, writeErr := destination.Write(buffer[:read])
			written += int64(count)
			if writeErr != nil {
				return written, hiddenError("could not inspect backup artifact", writeErr)
			}
			if count != read {
				return written, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, hiddenError("could not inspect backup artifact", readErr)
		}
	}
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

type redactedError struct {
	message string
	cause   error
}

func (e *redactedError) Error() string { return e.message }
func (e *redactedError) Unwrap() error { return e.cause }

func hiddenError(message string, cause error) error {
	return &redactedError{message: message, cause: cause}
}

var _ storage.Store = (*Store)(nil)

func (s *Store) Delete(context.Context, string) error {
	return fmt.Errorf("remote retention is not implemented")
}

func (s *Store) ListBackupSets(context.Context, string) ([]storage.BackupSet, error) {
	return nil, fmt.Errorf("remote retention is not implemented")
}
