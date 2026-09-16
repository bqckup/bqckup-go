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
			Contents:              []types.Object{{Key: aws.String("company/bqckup/site/2026-08-01/00-00-00-files.tar.gz")}},
			IsTruncated:           aws.Bool(true),
			NextContinuationToken: aws.String("next"),
		},
		{Contents: []types.Object{
			{Key: aws.String("company/bqckup/site/2026-08-02/00-00-00-files.tar.gz")},
			{Key: aws.String("company/bqckup/site/not-a-date/bad")},
			{Key: aws.String("company/bqckup/other/2026-08-03/00-00-00-files.tar.gz")},
		}},
	}}
	store := newWithClients(Options{Bucket: "backups", Prefix: "company"}, &fakeUploader{}, client, nil)

	sets, err := store.ListBackupSets(context.Background(), "bqckup/site")
	require.NoError(t, err)
	require.Len(t, sets, 2)
	assert.Equal(t, "bqckup/site/2026-08-01/00-00-00", sets[0].Key)
	assert.Equal(t, "bqckup/site/2026-08-02/00-00-00", sets[1].Key)
	require.Len(t, client.listInputs, 2)
	assert.Equal(t, "company/bqckup/site/", aws.ToString(client.listInputs[0].Prefix))
	assert.Equal(t, "next", aws.ToString(client.listInputs[1].ContinuationToken))
}

func TestListBackupSetsMarksOnlyCompletedFlatRuns(t *testing.T) {
	client := &fakeClient{listOutputs: []*s3.ListObjectsV2Output{
		{Contents: []types.Object{
			{Key: aws.String("company/bqckup/site/2026-09-16/01-00-00-aaaaaaaa-files.tar.gz")},
			{Key: aws.String("company/bqckup/site/2026-09-16/01-00-00-aaaaaaaa-.bqckup-complete")},
			{Key: aws.String("company/bqckup/site/2026-09-16/02-00-00-bbbbbbbb-files.tar.gz")},
		}},
	}}
	store := newWithClients(Options{Bucket: "backups", Prefix: "company"}, &fakeUploader{}, client, nil)

	sets, err := store.ListBackupSets(context.Background(), "bqckup/site")
	require.NoError(t, err)
	require.Len(t, sets, 2)
	assert.True(t, sets[0].Complete)
	assert.False(t, sets[1].Complete)
}

func TestListBackupSetsAcceptsConfiguredBackupNamespace(t *testing.T) {
	client := &fakeClient{listOutputs: []*s3.ListObjectsV2Output{
		{Contents: []types.Object{
			{Key: aws.String("bqckup/hosting_client/production/45.146.6.26_7bjym/abdimas.poltekparmedan.ac.id/16-September-2026/08-08-06-34091879-files.tar.gz")},
			{Key: aws.String("bqckup/hosting_client/production/45.146.6.26_7bjym/abdimas.poltekparmedan.ac.id/16-September-2026/08-08-06-34091879-.bqckup-complete")},
		}},
	}}
	store := newWithClients(Options{Bucket: "backups"}, &fakeUploader{}, client, nil)

	sets, err := store.ListBackupSets(context.Background(), "bqckup/hosting_client/production/45.146.6.26_7bjym/abdimas.poltekparmedan.ac.id")

	require.NoError(t, err)
	require.Len(t, sets, 1)
	assert.Equal(t, "bqckup/hosting_client/production/45.146.6.26_7bjym/abdimas.poltekparmedan.ac.id/16-September-2026/08-08-06-34091879", sets[0].Key)
	assert.True(t, sets[0].Complete)
}

func TestDeletePaginatesAndBatchesAtOneThousand(t *testing.T) {
	objects := make([]types.Object, 1001)
	for index := range objects {
		objects[index].Key = aws.String(fmt.Sprintf("company/bqckup/site/2026-08-01T00-00-00.000000000Z/file-%04d", index))
	}
	client := &fakeClient{listOutputs: []*s3.ListObjectsV2Output{{Contents: objects}}}
	store := newWithClients(Options{Bucket: "backups", Prefix: "company"}, &fakeUploader{}, client, nil)

	require.NoError(t, store.Delete(context.Background(), "bqckup/site/2026-08-01T00-00-00.000000000Z"))
	require.Len(t, client.deleteObjectsInputs, 2)
	assert.Len(t, client.deleteObjectsInputs[0].Delete.Objects, 1000)
	assert.Len(t, client.deleteObjectsInputs[1].Delete.Objects, 1)
	assert.Equal(t, "company/bqckup/site/2026-08-01T00-00-00.000000000Z/", aws.ToString(client.listInputs[0].Prefix))
}

func TestDeleteAcceptsReadableBackupSetPrefix(t *testing.T) {
	client := &fakeClient{listOutputs: []*s3.ListObjectsV2Output{{}}}
	store := newWithClients(Options{Bucket: "backups", Prefix: "company"}, &fakeUploader{}, client, nil)

	require.NoError(t, store.Delete(context.Background(), "bqckup/site/2026-08-01/00-00-00"))
	require.Len(t, client.listInputs, 1)
	assert.Equal(t, "company/bqckup/site/2026-08-01/", aws.ToString(client.listInputs[0].Prefix))
}

func TestDeleteAcceptsConfiguredBackupNamespace(t *testing.T) {
	client := &fakeClient{listOutputs: []*s3.ListObjectsV2Output{{}}}
	store := newWithClients(Options{Bucket: "backups"}, &fakeUploader{}, client, nil)

	require.NoError(t, store.Delete(context.Background(), "bqckup/hosting_client/production/45.146.6.26_7bjym/abdimas.poltekparmedan.ac.id/16-September-2026/08-08-06-34091879"))
	require.Len(t, client.listInputs, 1)
	assert.Equal(t, "bqckup/hosting_client/production/45.146.6.26_7bjym/abdimas.poltekparmedan.ac.id/16-September-2026/", aws.ToString(client.listInputs[0].Prefix))
}

func TestDeleteRejectsUnsafeOrBroadPrefixes(t *testing.T) {
	store := newWithClients(Options{Bucket: "backups"}, &fakeUploader{}, &fakeClient{}, nil)
	for _, prefix := range []string{"", "bqckup", "bqckup/site", "bqckup/site/not-a-date", "../escape"} {
		t.Run(prefix, func(t *testing.T) {
			require.Error(t, store.Delete(context.Background(), prefix))
		})
	}
}
