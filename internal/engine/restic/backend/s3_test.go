package backend

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/bqckup/bqckup-go/internal/engine/restic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testS3Options() S3Options {
	return S3Options{Bucket: "backups", Prefix: "company/restic/site-a"}
}

func TestS3KeyMapping(t *testing.T) {
	b := newS3WithClients(testS3Options(), &fakeS3Uploader{}, &fakeS3Client{})

	cases := []struct {
		handle restic.Handle
		want   string
	}{
		{restic.Handle{Type: restic.ConfigFile}, "company/restic/site-a/config"},
		{restic.Handle{Type: restic.KeyFileType, Name: strings.Repeat("a", 64)}, "company/restic/site-a/keys/" + strings.Repeat("a", 64)},
		{restic.Handle{Type: restic.IndexFile, Name: strings.Repeat("b", 64)}, "company/restic/site-a/index/" + strings.Repeat("b", 64)},
		{restic.Handle{Type: restic.SnapshotFile, Name: strings.Repeat("c", 64)}, "company/restic/site-a/snapshots/" + strings.Repeat("c", 64)},
		{restic.Handle{Type: restic.LockFile, Name: strings.Repeat("d", 64)}, "company/restic/site-a/locks/" + strings.Repeat("d", 64)},
		{restic.Handle{Type: restic.DataFile, Name: strings.Repeat("e", 64)}, "company/restic/site-a/data/ee/" + strings.Repeat("e", 64)},
	}
	for _, tc := range cases {
		key, err := b.keyFor(tc.handle)
		require.NoError(t, err)
		assert.Equal(t, tc.want, key)
	}

	_, err := b.keyFor(restic.Handle{Type: restic.DataFile, Name: "short"})
	require.Error(t, err, "data file names must be 64 hex characters")
}

func TestS3SaveUploadsToMappedKey(t *testing.T) {
	uploader := &fakeS3Uploader{}
	b := newS3WithClients(testS3Options(), uploader, &fakeS3Client{})

	err := b.Save(context.Background(), restic.Handle{Type: restic.IndexFile, Name: strings.Repeat("b", 64)}, strings.NewReader("payload"))
	require.NoError(t, err)
	require.NotNil(t, uploader.input)
	assert.Equal(t, "backups", aws.ToString(uploader.input.Bucket))
	assert.Equal(t, "company/restic/site-a/index/"+strings.Repeat("b", 64), aws.ToString(uploader.input.Key))
	assert.Nil(t, uploader.input.IfNoneMatch, "repository objects are content-addressed, not conditional")
}

func TestS3SaveHidesProviderError(t *testing.T) {
	b := newS3WithClients(testS3Options(), &fakeS3Uploader{err: &smithy.GenericAPIError{Code: "InternalError", Message: "provider secret response"}}, &fakeS3Client{})

	err := b.Save(context.Background(), restic.Handle{Type: restic.IndexFile, Name: strings.Repeat("b", 64)}, strings.NewReader("payload"))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "provider secret response")
}

func TestS3LoadUsesRangeAndStreams(t *testing.T) {
	client := &fakeS3Client{getOutput: &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader("world"))}}
	b := newS3WithClients(testS3Options(), &fakeS3Uploader{}, client)

	var got []byte
	err := b.Load(context.Background(), restic.Handle{Type: restic.DataFile, Name: strings.Repeat("e", 64)}, 5, 6, func(rd io.Reader) error {
		var readErr error
		got, readErr = io.ReadAll(rd)
		return readErr
	})
	require.NoError(t, err)
	assert.Equal(t, "world", string(got))
	require.NotNil(t, client.getInput)
	assert.Equal(t, "bytes=6-10", aws.ToString(client.getInput.Range))
	assert.Equal(t, "company/restic/site-a/data/ee/"+strings.Repeat("e", 64), aws.ToString(client.getInput.Key))
}

func TestS3LoadOpenEndedRange(t *testing.T) {
	client := &fakeS3Client{getOutput: &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader("rest"))}}
	b := newS3WithClients(testS3Options(), &fakeS3Uploader{}, client)

	err := b.Load(context.Background(), restic.Handle{Type: restic.ConfigFile}, 0, 4, func(rd io.Reader) error {
		_, err := io.ReadAll(rd)
		return err
	})
	require.NoError(t, err)
	require.NotNil(t, client.getInput)
	assert.Equal(t, "bytes=4-", aws.ToString(client.getInput.Range))
}

func TestS3LoadMissingIsNotExist(t *testing.T) {
	client := &fakeS3Client{getErr: &smithy.GenericAPIError{Code: "NoSuchKey", Message: "no such object"}}
	b := newS3WithClients(testS3Options(), &fakeS3Uploader{}, client)

	err := b.Load(context.Background(), restic.Handle{Type: restic.ConfigFile}, 0, 0, func(rd io.Reader) error { return nil })
	require.Error(t, err)
	assert.True(t, b.IsNotExist(err))
}

func TestS3Stat(t *testing.T) {
	client := &fakeS3Client{headOutput: &s3.HeadObjectOutput{ContentLength: aws.Int64(4321)}}
	b := newS3WithClients(testS3Options(), &fakeS3Uploader{}, client)

	info, err := b.Stat(context.Background(), restic.Handle{Type: restic.SnapshotFile, Name: strings.Repeat("c", 64)})
	require.NoError(t, err)
	assert.Equal(t, int64(4321), info.Size)
	assert.Equal(t, strings.Repeat("c", 64), info.Name)
	require.NotNil(t, client.headInput)
	assert.Equal(t, "company/restic/site-a/snapshots/"+strings.Repeat("c", 64), aws.ToString(client.headInput.Key))

	client.headErr = &smithy.GenericAPIError{Code: "NotFound", Message: "missing"}
	_, err = b.Stat(context.Background(), restic.Handle{Type: restic.SnapshotFile, Name: strings.Repeat("c", 64)})
	assert.True(t, b.IsNotExist(err))
}

func TestS3ListTypesAndPagination(t *testing.T) {
	client := &fakeS3Client{listOutputs: []*s3.ListObjectsV2Output{
		{
			IsTruncated:           aws.Bool(true),
			NextContinuationToken: aws.String("next-page"),
			Contents: []types.Object{
				{Key: aws.String("company/restic/site-a/index/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), Size: aws.Int64(11)},
				{Key: aws.String("company/restic/site-a/index/foreign/object"), Size: aws.Int64(3)},
			},
		},
		{
			Contents: []types.Object{
				{Key: aws.String("company/restic/site-a/index/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), Size: aws.Int64(22)},
			},
		},
	}}
	b := newS3WithClients(testS3Options(), &fakeS3Uploader{}, client)

	var names []string
	var sizes []int64
	err := b.List(context.Background(), restic.IndexFile, func(h restic.Handle, size int64) error {
		names = append(names, h.Name)
		sizes = append(sizes, size)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}, names)
	assert.Equal(t, []int64{11, 22}, sizes)
	require.Len(t, client.listInputs, 2)
	assert.Equal(t, "company/restic/site-a/index/", aws.ToString(client.listInputs[0].Prefix))
	assert.Equal(t, "next-page", aws.ToString(client.listInputs[1].ContinuationToken))
}

func TestS3ListDataUsesLastSegment(t *testing.T) {
	client := &fakeS3Client{listOutputs: []*s3.ListObjectsV2Output{
		{Contents: []types.Object{
			{Key: aws.String("company/restic/site-a/data/ab/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), Size: aws.Int64(7)},
		}},
	}}
	b := newS3WithClients(testS3Options(), &fakeS3Uploader{}, client)

	var names []string
	err := b.List(context.Background(), restic.DataFile, func(h restic.Handle, size int64) error {
		names = append(names, h.Name)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{strings.Repeat("a", 64)}, names)
	assert.Equal(t, "company/restic/site-a/data/", aws.ToString(client.listInputs[0].Prefix))
}

func TestS3Remove(t *testing.T) {
	client := &fakeS3Client{}
	b := newS3WithClients(testS3Options(), &fakeS3Uploader{}, client)

	err := b.Remove(context.Background(), restic.Handle{Type: restic.LockFile, Name: strings.Repeat("d", 64)})
	require.NoError(t, err)
	require.NotNil(t, client.deleteInput)
	assert.Equal(t, "company/restic/site-a/locks/"+strings.Repeat("d", 64), aws.ToString(client.deleteInput.Key))
}

func TestS3IsNotExist(t *testing.T) {
	b := newS3WithClients(testS3Options(), &fakeS3Uploader{}, &fakeS3Client{})
	for _, code := range []string{"NoSuchKey", "NotFound"} {
		assert.True(t, b.IsNotExist(&smithy.GenericAPIError{Code: code}))
	}
	assert.True(t, b.IsNotExist(&statusError{status: 404}))
	assert.False(t, b.IsNotExist(&smithy.GenericAPIError{Code: "InternalError"}))
	assert.False(t, b.IsNotExist(nil))
	assert.False(t, b.IsNotExist(errors.New("plain error")))
}

func TestS3PreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &fakeS3Client{getErr: context.Canceled}
	b := newS3WithClients(testS3Options(), &fakeS3Uploader{}, client)

	err := b.Load(ctx, restic.Handle{Type: restic.ConfigFile}, 0, 0, func(rd io.Reader) error { return nil })
	require.ErrorIs(t, err, context.Canceled)
}

// --- fakes ---

type fakeS3Uploader struct {
	input *transfermanager.UploadObjectInput
	err   error
}

func (f *fakeS3Uploader) UploadObject(_ context.Context, input *transfermanager.UploadObjectInput, _ ...func(*transfermanager.Options)) (*transfermanager.UploadObjectOutput, error) {
	f.input = input
	return &transfermanager.UploadObjectOutput{}, f.err
}

type fakeS3Client struct {
	headOutput  *s3.HeadObjectOutput
	headErr     error
	headInput   *s3.HeadObjectInput
	getOutput   *s3.GetObjectOutput
	getErr      error
	getInput    *s3.GetObjectInput
	deleteInput *s3.DeleteObjectInput
	deleteErr   error
	listOutputs []*s3.ListObjectsV2Output
	listErr     error
	listInputs  []*s3.ListObjectsV2Input
}

func (f *fakeS3Client) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.headInput = input
	return f.headOutput, f.headErr
}

func (f *fakeS3Client) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.getInput = input
	return f.getOutput, f.getErr
}

func (f *fakeS3Client) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.deleteInput = input
	return &s3.DeleteObjectOutput{}, f.deleteErr
}

func (f *fakeS3Client) ListObjectsV2(_ context.Context, input *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
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

type statusError struct{ status int }

func (e *statusError) Error() string       { return "status error" }
func (e *statusError) HTTPStatusCode() int { return e.status }
