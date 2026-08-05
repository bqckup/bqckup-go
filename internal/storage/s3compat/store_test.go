package s3compat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/bqckup/bqckup-go/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPutUploadsConditionallyAndVerifiesMetadata(t *testing.T) {
	artifact := sourceArtifact(t, []byte("verified backup"))
	uploader := &fakeUploader{}
	client := &fakeClient{headOutput: verifiedHead(artifact)}
	store := newWithClients(Options{Provider: ProviderS3, Bucket: "backups", Region: "us-east-1", Prefix: "company"}, uploader, client)

	stored, err := store.Put(context.Background(), artifact, "bqckup/site/2026-08-05T00-00-00Z/files.tar.gz")
	require.NoError(t, err)
	require.NotNil(t, uploader.input)
	assert.Equal(t, "*", aws.ToString(uploader.input.IfNoneMatch))
	assert.Equal(t, "company/bqckup/site/2026-08-05T00-00-00Z/files.tar.gz", aws.ToString(uploader.input.Key))
	assert.Equal(t, artifact.Size, aws.ToInt64(uploader.input.MpuObjectSize))
	assert.Equal(t, artifact.SHA256, uploader.input.Metadata[checksumMetadata])
	assert.Equal(t, strconv.FormatInt(artifact.Size, 10), uploader.input.Metadata[sizeMetadata])
	assert.Equal(t, artifact.SHA256, stored.SHA256)
	assert.Equal(t, artifact.Size, stored.Size)
}

func TestPutRejectsLocalArtifactMismatchBeforeUpload(t *testing.T) {
	artifact := sourceArtifact(t, []byte("actual"))
	artifact.SHA256 = hex.EncodeToString(make([]byte, sha256.Size))
	uploader := &fakeUploader{}
	store := newWithClients(Options{Bucket: "backups"}, uploader, &fakeClient{})

	_, err := store.Put(context.Background(), artifact, "bqckup/site/2026-08-05T00-00-00Z/files.tar.gz")
	require.Error(t, err)
	assert.Nil(t, uploader.input)
}

func TestPutPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := newWithClients(Options{Bucket: "backups"}, &fakeUploader{}, &fakeClient{})

	_, err := store.Put(ctx, sourceArtifact(t, []byte("backup")), "bqckup/site/2026-08-05T00-00-00Z/files.tar.gz")
	require.ErrorIs(t, err, context.Canceled)
}

func TestPutReturnsStableCollisionError(t *testing.T) {
	artifact := sourceArtifact(t, []byte("backup"))
	uploader := &fakeUploader{err: &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "provider secret response"}}
	store := newWithClients(Options{Bucket: "backups"}, uploader, &fakeClient{})

	_, err := store.Put(context.Background(), artifact, "bqckup/site/2026-08-05T00-00-00Z/files.tar.gz")
	require.ErrorIs(t, err, ErrObjectExists)
	assert.NotContains(t, err.Error(), "provider secret response")
}

func TestPutCleansUpExactObjectWhenRemoteVerificationFails(t *testing.T) {
	artifact := sourceArtifact(t, []byte("backup"))
	client := &fakeClient{headOutput: &s3.HeadObjectOutput{
		ContentLength: aws.Int64(artifact.Size + 1),
		Metadata:      map[string]string{checksumMetadata: artifact.SHA256, sizeMetadata: strconv.FormatInt(artifact.Size, 10)},
	}}
	store := newWithClients(Options{Bucket: "backups", Prefix: "company"}, &fakeUploader{}, client)

	_, err := store.Put(context.Background(), artifact, "bqckup/site/2026-08-05T00-00-00Z/files.tar.gz")
	require.Error(t, err)
	require.NotNil(t, client.deleteInput)
	assert.Equal(t, "company/bqckup/site/2026-08-05T00-00-00Z/files.tar.gz", aws.ToString(client.deleteInput.Key))
}

func verifiedHead(artifact storage.Artifact) *s3.HeadObjectOutput {
	return &s3.HeadObjectOutput{
		ContentLength: aws.Int64(artifact.Size),
		Metadata: map[string]string{
			checksumMetadata: artifact.SHA256,
			sizeMetadata:     strconv.FormatInt(artifact.Size, 10),
		},
	}
}

type fakeUploader struct {
	input *transfermanager.UploadObjectInput
	err   error
}

func (f *fakeUploader) UploadObject(_ context.Context, input *transfermanager.UploadObjectInput, _ ...func(*transfermanager.Options)) (*transfermanager.UploadObjectOutput, error) {
	f.input = input
	return &transfermanager.UploadObjectOutput{}, f.err
}

type fakeClient struct {
	headOutput          *s3.HeadObjectOutput
	headErr             error
	deleteInput         *s3.DeleteObjectInput
	deleteErr           error
	listOutputs         []*s3.ListObjectsV2Output
	listErr             error
	listInputs          []*s3.ListObjectsV2Input
	deleteObjectsInputs []*s3.DeleteObjectsInput
	deleteObjectsOutput *s3.DeleteObjectsOutput
	deleteObjectsErr    error
}

func (f *fakeClient) HeadObject(_ context.Context, _ *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return f.headOutput, f.headErr
}

func (f *fakeClient) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.deleteInput = input
	return &s3.DeleteObjectOutput{}, f.deleteErr
}

func (f *fakeClient) ListObjectsV2(_ context.Context, input *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.listInputs = append(f.listInputs, input)
	if f.listErr != nil {
		return nil, f.listErr
	}
	if len(f.listOutputs) == 0 {
		return &s3.ListObjectsV2Output{}, nil
	}
	output := f.listOutputs[0]
	f.listOutputs = f.listOutputs[1:]
	return output, nil
}

func (f *fakeClient) DeleteObjects(_ context.Context, input *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	f.deleteObjectsInputs = append(f.deleteObjectsInputs, input)
	if f.deleteObjectsOutput == nil {
		return &s3.DeleteObjectsOutput{}, f.deleteObjectsErr
	}
	return f.deleteObjectsOutput, f.deleteObjectsErr
}

func sourceArtifact(t *testing.T, contents []byte) storage.Artifact {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artifact.tar.gz")
	require.NoError(t, os.WriteFile(path, contents, 0o600))
	sum := sha256.Sum256(contents)
	return storage.Artifact{Path: path, Size: int64(len(contents)), SHA256: hex.EncodeToString(sum[:])}
}
