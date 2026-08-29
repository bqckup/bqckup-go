package s3compat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/bqckup/bqckup-go/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPutUploadsConditionallyAndVerifiesMetadata(t *testing.T) {
	pkg := sourcePackage(t, []byte("verified backup"))
	uploader := &fakeUploader{}
	client := &fakeClient{headOutput: verifiedHead(pkg)}
	store := newWithClients(Options{Provider: ProviderS3, Bucket: "backups", Region: "us-east-1", Prefix: "company"}, uploader, client, nil)

	stored, err := store.Put(context.Background(), pkg, "bqckup/site/2026-08-05T00-00-00Z/files.tar.gz")
	require.NoError(t, err)
	require.NotNil(t, uploader.input)
	assert.Equal(t, "*", aws.ToString(uploader.input.IfNoneMatch))
	assert.Equal(t, "company/bqckup/site/2026-08-05T00-00-00Z/files.tar.gz", aws.ToString(uploader.input.Key))
	assert.Equal(t, pkg.Size, aws.ToInt64(uploader.input.MpuObjectSize))
	assert.Equal(t, pkg.SHA256, uploader.input.Metadata[checksumMetadata])
	assert.Equal(t, strconv.FormatInt(pkg.Size, 10), uploader.input.Metadata[sizeMetadata])
	assert.Equal(t, pkg.SHA256, stored.SHA256)
	assert.Equal(t, pkg.Size, stored.Size)
}

func TestPutRejectsLocalPackageMismatchBeforeUpload(t *testing.T) {
	pkg := sourcePackage(t, []byte("actual"))
	pkg.SHA256 = hex.EncodeToString(make([]byte, sha256.Size))
	uploader := &fakeUploader{}
	store := newWithClients(Options{Bucket: "backups"}, uploader, &fakeClient{}, nil)

	_, err := store.Put(context.Background(), pkg, "bqckup/site/2026-08-05T00-00-00Z/files.tar.gz")
	require.Error(t, err)
	assert.Nil(t, uploader.input)
}

func TestPutPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := newWithClients(Options{Bucket: "backups"}, &fakeUploader{}, &fakeClient{}, nil)

	_, err := store.Put(ctx, sourcePackage(t, []byte("backup")), "bqckup/site/2026-08-05T00-00-00Z/files.tar.gz")
	require.ErrorIs(t, err, context.Canceled)
}

func TestPutReturnsStableCollisionError(t *testing.T) {
	pkg := sourcePackage(t, []byte("backup"))
	uploader := &fakeUploader{err: &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "provider secret response"}}
	store := newWithClients(Options{Bucket: "backups"}, uploader, &fakeClient{}, nil)

	_, err := store.Put(context.Background(), pkg, "bqckup/site/2026-08-05T00-00-00Z/files.tar.gz")
	require.ErrorIs(t, err, ErrObjectExists)
	assert.NotContains(t, err.Error(), "provider secret response")
}

func TestPutCleansUpExactObjectWhenRemoteVerificationFails(t *testing.T) {
	pkg := sourcePackage(t, []byte("backup"))
	client := &fakeClient{headOutput: &s3.HeadObjectOutput{
		ContentLength: aws.Int64(pkg.Size + 1),
		Metadata:      map[string]string{checksumMetadata: pkg.SHA256, sizeMetadata: strconv.FormatInt(pkg.Size, 10)},
	}}
	store := newWithClients(Options{Bucket: "backups", Prefix: "company"}, &fakeUploader{}, client, nil)

	_, err := store.Put(context.Background(), pkg, "bqckup/site/2026-08-05T00-00-00Z/files.tar.gz")
	require.Error(t, err)
	require.NotNil(t, client.deleteInput)
	assert.Equal(t, "company/bqckup/site/2026-08-05T00-00-00Z/files.tar.gz", aws.ToString(client.deleteInput.Key))
}

func verifiedHead(pkg storage.Package) *s3.HeadObjectOutput {
	return &s3.HeadObjectOutput{
		ContentLength: aws.Int64(pkg.Size),
		Metadata: map[string]string{
			checksumMetadata: pkg.SHA256,
			sizeMetadata:     strconv.FormatInt(pkg.Size, 10),
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
	headInput           *s3.HeadObjectInput
	deleteInput         *s3.DeleteObjectInput
	deleteErr           error
	listOutputs         []*s3.ListObjectsV2Output
	listErr             error
	listInputs          []*s3.ListObjectsV2Input
	deleteObjectsInputs []*s3.DeleteObjectsInput
	deleteObjectsOutput *s3.DeleteObjectsOutput
	deleteObjectsErr    error
}

func (f *fakeClient) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.headInput = input
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

func TestListPackagesReturnsEveryObjectUnderTheSet(t *testing.T) {
	created := time.Date(2026, 11, 10, 3, 0, 12, 0, time.UTC)
	client := &fakeClient{listOutputs: []*s3.ListObjectsV2Output{
		{Contents: []types.Object{
			{Key: aws.String("company/bqckup/site-a/2026-11-10T03-00-00.000000000Z/files.tar.gz"), Size: aws.Int64(100), LastModified: aws.Time(created)},
			{Key: aws.String("company/bqckup/site-a/2026-11-10T03-00-00.000000000Z/databases/db.sql.gz"), Size: aws.Int64(50), LastModified: aws.Time(created.Add(time.Second))},
		}, IsTruncated: aws.Bool(true), NextContinuationToken: aws.String("next")},
		{Contents: []types.Object{
			{Key: aws.String("company/bqckup/site-a/2026-11-10T03-00-00.000000000Z/databases/db2.sql.gz"), Size: aws.Int64(25), LastModified: aws.Time(created.Add(2 * time.Second))},
		}, IsTruncated: aws.Bool(false)},
	}}
	store := newWithClients(Options{Bucket: "backups", Prefix: "company"}, &fakeUploader{}, client, nil)

	packages, err := store.ListPackages(context.Background(), "bqckup/site-a/2026-11-10T03-00-00.000000000Z")
	require.NoError(t, err)
	require.Len(t, packages, 3)
	assert.Equal(t, "bqckup/site-a/2026-11-10T03-00-00.000000000Z/files.tar.gz", packages[0].Key)
	assert.Equal(t, int64(100), packages[0].Size)
	assert.Equal(t, created, packages[0].CreatedAt)
	assert.Equal(t, "bqckup/site-a/2026-11-10T03-00-00.000000000Z/databases/db.sql.gz", packages[1].Key)
	assert.Equal(t, "bqckup/site-a/2026-11-10T03-00-00.000000000Z/databases/db2.sql.gz", packages[2].Key)
	assert.Equal(t, int64(25), packages[2].Size)
	require.Len(t, client.listInputs, 2)
	assert.Equal(t, "company/bqckup/site-a/2026-11-10T03-00-00.000000000Z/", aws.ToString(client.listInputs[0].Prefix))
	assert.Equal(t, "next", aws.ToString(client.listInputs[1].ContinuationToken))
}

func TestListPackagesSkipsKeysOutsideTheSet(t *testing.T) {
	created := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	client := &fakeClient{listOutputs: []*s3.ListObjectsV2Output{
		{Contents: []types.Object{
			{Key: aws.String("company/bqckup/site-a/2026-11-10T03-00-00.000000000Z/files.tar.gz"), Size: aws.Int64(1), LastModified: aws.Time(created)},
			// Foreign keys must not surface: another set, another site, a restic blob prefix.
			{Key: aws.String("company/bqckup/site-a/2026-11-11T03-00-00.000000000Z/files.tar.gz"), Size: aws.Int64(2), LastModified: aws.Time(created)},
			{Key: aws.String("company/bqckup/site-b/2026-11-10T03-00-00.000000000Z/files.tar.gz"), Size: aws.Int64(3), LastModified: aws.Time(created)},
		}, IsTruncated: aws.Bool(false)},
	}}
	store := newWithClients(Options{Bucket: "backups", Prefix: "company"}, &fakeUploader{}, client, nil)

	packages, err := store.ListPackages(context.Background(), "bqckup/site-a/2026-11-10T03-00-00.000000000Z")
	require.NoError(t, err)
	require.Len(t, packages, 1)
	assert.Equal(t, "bqckup/site-a/2026-11-10T03-00-00.000000000Z/files.tar.gz", packages[0].Key)
}

func TestListPackagesRejectsInvalidPrefixes(t *testing.T) {
	store := newWithClients(Options{Bucket: "backups"}, &fakeUploader{}, &fakeClient{}, nil)
	for _, invalid := range []string{
		"bqckup",
		"bqckup/site-a",
		"bqckup/site-a/not-a-timestamp",
		"bqckup/site-a/2026-11-10T03-00-00.000000000Z/",
		"../bqckup/site-a/2026-11-10T03-00-00.000000000Z",
	} {
		_, err := store.ListPackages(context.Background(), invalid)
		assert.Error(t, err, invalid)
	}
}

func TestListPackagesPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := newWithClients(Options{Bucket: "backups"}, &fakeUploader{}, &fakeClient{}, nil)
	_, err := store.ListPackages(ctx, "bqckup/site-a/2026-11-10T03-00-00.000000000Z")
	require.ErrorIs(t, err, context.Canceled)
}

func TestListPackagesRedactsProviderErrors(t *testing.T) {
	client := &fakeClient{listErr: &smithy.GenericAPIError{Code: "AccessDenied", Message: "provider secret response"}}
	store := newWithClients(Options{Bucket: "backups"}, &fakeUploader{}, client, nil)
	_, err := store.ListPackages(context.Background(), "bqckup/site-a/2026-11-10T03-00-00.000000000Z")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "provider secret response")
}

func TestListPackagesRejectsMissingContinuationToken(t *testing.T) {
	client := &fakeClient{listOutputs: []*s3.ListObjectsV2Output{
		{Contents: []types.Object{}, IsTruncated: aws.Bool(true)},
	}}
	store := newWithClients(Options{Bucket: "backups"}, &fakeUploader{}, client, nil)
	_, err := store.ListPackages(context.Background(), "bqckup/site-a/2026-11-10T03-00-00.000000000Z")
	require.Error(t, err)
}

func sourcePackage(t *testing.T, contents []byte) storage.Package {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pkg.tar.gz")
	require.NoError(t, os.WriteFile(path, contents, 0o600))
	sum := sha256.Sum256(contents)
	return storage.Package{Path: path, Size: int64(len(contents)), SHA256: hex.EncodeToString(sum[:])}
}

func testPresigner(t *testing.T) presignerAPI {
	t.Helper()
	sdkConfig, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test-key", "test-secret", "")),
	)
	require.NoError(t, err)
	return s3.NewPresignClient(s3.NewFromConfig(sdkConfig))
}

func TestPresignLinkReturnsSignedURLForExistingObject(t *testing.T) {
	client := &fakeClient{headOutput: &s3.HeadObjectOutput{}}
	store := newWithClients(Options{Bucket: "backups", Prefix: "company"}, &fakeUploader{}, client, testPresigner(t))

	link, err := store.PresignLink(context.Background(), "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz", 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz", link.Key)
	require.NotNil(t, client.headInput)
	assert.Equal(t, "company/bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz", aws.ToString(client.headInput.Key))
	assert.WithinDuration(t, time.Now().Add(24*time.Hour), link.ExpiresAt, 2*time.Second)

	parsed, err := url.Parse(link.URL)
	require.NoError(t, err)
	query := parsed.Query()
	assert.Equal(t, "86400", query.Get("X-Amz-Expires"))
	assert.Equal(t, "attachment; filename=files.tar.gz", query.Get("response-content-disposition"))
	assert.NotEmpty(t, query.Get("X-Amz-Signature"))
}

func TestPresignLinkReportsMissingObjectByName(t *testing.T) {
	client := &fakeClient{headErr: &smithy.GenericAPIError{Code: "NotFound", Message: "provider secret response"}}
	store := newWithClients(Options{Bucket: "backups"}, &fakeUploader{}, client, testPresigner(t))

	_, err := store.PresignLink(context.Background(), "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz", time.Hour)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "files.tar.gz")
	assert.NotContains(t, err.Error(), "provider secret response")
}

func TestPresignLinkRedactsOtherHeadErrors(t *testing.T) {
	client := &fakeClient{headErr: &smithy.GenericAPIError{Code: "AccessDenied", Message: "provider secret response"}}
	store := newWithClients(Options{Bucket: "backups"}, &fakeUploader{}, client, testPresigner(t))

	_, err := store.PresignLink(context.Background(), "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz", time.Hour)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "provider secret response")
	assert.NotContains(t, err.Error(), "files.tar.gz")
}

func TestPresignLinkPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &fakeClient{}
	store := newWithClients(Options{Bucket: "backups"}, &fakeUploader{}, client, testPresigner(t))

	_, err := store.PresignLink(ctx, "bqckup/site-a/2026-08-05T00-00-00Z/files.tar.gz", time.Hour)
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, client.headInput)
}

func TestPresignLinkRejectsUnsafeKeys(t *testing.T) {
	client := &fakeClient{}
	store := newWithClients(Options{Bucket: "backups"}, &fakeUploader{}, client, testPresigner(t))

	_, err := store.PresignLink(context.Background(), "../bqckup/site-a/files.tar.gz", time.Hour)
	require.Error(t, err)
	assert.Nil(t, client.headInput)
}

func TestStoreProbe(t *testing.T) {
	t.Run("lists one object under the storage prefix", func(t *testing.T) {
		client := &fakeClient{}
		store := newWithClients(Options{Bucket: "backups", Prefix: "company"}, &fakeUploader{}, client, nil)

		require.NoError(t, store.Probe(context.Background()))
		require.Len(t, client.listInputs, 1)
		assert.Equal(t, "backups", aws.ToString(client.listInputs[0].Bucket))
		assert.Equal(t, "company", aws.ToString(client.listInputs[0].Prefix))
		assert.Equal(t, int32(1), aws.ToInt32(client.listInputs[0].MaxKeys))
		assert.Nil(t, client.headInput, "probe must only list, never head or delete")
		assert.Nil(t, client.deleteInput)
	})

	t.Run("returns the API error code only", func(t *testing.T) {
		client := &fakeClient{listErr: &smithy.GenericAPIError{Code: "AccessDenied", Message: "provider secret response"}}
		store := newWithClients(Options{Bucket: "backups"}, &fakeUploader{}, client, nil)

		err := store.Probe(context.Background())
		require.Error(t, err)
		assert.Equal(t, "AccessDenied", err.Error())
		assert.NotContains(t, err.Error(), "provider secret response")
	})

	t.Run("hides non-smithy errors", func(t *testing.T) {
		client := &fakeClient{listErr: errors.New("dial tcp: lookup secret-endpoint.example")}
		store := newWithClients(Options{Bucket: "backups"}, &fakeUploader{}, client, nil)

		err := store.Probe(context.Background())
		require.Error(t, err)
		assert.Equal(t, "request failed", err.Error())
	})

	t.Run("reports timeouts plainly", func(t *testing.T) {
		client := &fakeClient{listErr: context.DeadlineExceeded}
		store := newWithClients(Options{Bucket: "backups"}, &fakeUploader{}, client, nil)

		err := store.Probe(context.Background())
		require.Error(t, err)
		assert.Equal(t, "timed out", err.Error())
	})

	t.Run("preserves cancellation without calling the client", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client := &fakeClient{}
		store := newWithClients(Options{Bucket: "backups"}, &fakeUploader{}, client, nil)

		require.ErrorIs(t, store.Probe(ctx), context.Canceled)
		assert.Empty(t, client.listInputs)
	})
}
