package s3compat

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListBackupSetsPaginatesAndFilters(t *testing.T) {
	client := &fakeClient{listOutputs: []*s3.ListObjectsV2Output{
		{
			Contents:              []types.Object{{Key: aws.String("company/bqckup/site/2026-08-01T00-00-00Z/files.tar.gz")}},
			IsTruncated:           aws.Bool(true),
			NextContinuationToken: aws.String("next"),
		},
		{Contents: []types.Object{
			{Key: aws.String("company/bqckup/site/2026-08-02T00-00-00Z/files.tar.gz")},
			{Key: aws.String("company/bqckup/site/not-a-date/files.tar.gz")},
			{Key: aws.String("company/bqckup/other/2026-08-03T00-00-00Z/files.tar.gz")},
		}},
	}}
	store := newWithClients(Options{Bucket: "backups", Prefix: "company"}, &fakeUploader{}, client)

	sets, err := store.ListBackupSets(context.Background(), "bqckup/site")
	require.NoError(t, err)
	require.Len(t, sets, 2)
	assert.Equal(t, "bqckup/site/2026-08-01T00-00-00Z", sets[0].Key)
	assert.Equal(t, "bqckup/site/2026-08-02T00-00-00Z", sets[1].Key)
	require.Len(t, client.listInputs, 2)
	assert.Equal(t, "company/bqckup/site/", aws.ToString(client.listInputs[0].Prefix))
	assert.Equal(t, "next", aws.ToString(client.listInputs[1].ContinuationToken))
}

func TestDeletePaginatesAndBatchesAtOneThousand(t *testing.T) {
	objects := make([]types.Object, 1001)
	for index := range objects {
		objects[index].Key = aws.String(fmt.Sprintf("company/bqckup/site/2026-08-01T00-00-00Z/file-%04d", index))
	}
	client := &fakeClient{listOutputs: []*s3.ListObjectsV2Output{{Contents: objects}}}
	store := newWithClients(Options{Bucket: "backups", Prefix: "company"}, &fakeUploader{}, client)

	require.NoError(t, store.Delete(context.Background(), "bqckup/site/2026-08-01T00-00-00Z"))
	require.Len(t, client.deleteObjectsInputs, 2)
	assert.Len(t, client.deleteObjectsInputs[0].Delete.Objects, 1000)
	assert.Len(t, client.deleteObjectsInputs[1].Delete.Objects, 1)
	assert.Equal(t, "company/bqckup/site/2026-08-01T00-00-00Z/", aws.ToString(client.listInputs[0].Prefix))
}

func TestDeleteRejectsUnsafeOrBroadPrefixes(t *testing.T) {
	store := newWithClients(Options{Bucket: "backups"}, &fakeUploader{}, &fakeClient{})
	for _, prefix := range []string{"", "bqckup", "bqckup/site", "bqckup/site/not-a-date", "../escape"} {
		t.Run(prefix, func(t *testing.T) {
			require.Error(t, store.Delete(context.Background(), prefix))
		})
	}
}
