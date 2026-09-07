package repository

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/bqckup/bqckup-go/internal/engine/incremental/backend"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepairIndexHealthyRebuild(t *testing.T) {
	ctx := context.Background()
	loc := backend.NewLocal(t.TempDir())
	repo, err := Init(ctx, loc, prunePassword)
	require.NoError(t, err)

	backupTestFiles(t, repo, map[string]string{
		"file1.txt": "hello world",
		"file2.txt": "another test content for repair index verification",
	}, []string{"site1"})

	// Verify we have packs and indexes
	origPacks := packHandles(t, loc)
	require.NotEmpty(t, origPacks)
	origIndexes := indexHandles(t, loc)
	require.NotEmpty(t, origIndexes)

	// Delete existing index files to simulate missing/corrupt index state
	for _, h := range origIndexes {
		require.NoError(t, loc.Remove(ctx, h))
	}
	require.Empty(t, indexHandles(t, loc))

	// Open for repair (bypassing index loading)
	repairRepo, err := OpenForRepair(ctx, loc, prunePassword)
	require.NoError(t, err)

	result, err := repairRepo.RepairIndex(ctx)
	require.NoError(t, err)
	assert.Equal(t, len(origPacks), result.PacksProcessed)
	assert.Greater(t, result.BlobsIndexed, 0)
	assert.Equal(t, 0, result.OldIndexesRemoved)
	assert.Equal(t, 1, result.NewIndexesWritten)

	// Verify newly written index file exists
	newIndexes := indexHandles(t, loc)
	require.Len(t, newIndexes, 1)

	// Open normally and run repository check to ensure everything is valid and consistent
	checkedRepo, findings, err := CheckOpen(ctx, loc, prunePassword)
	require.NoError(t, err)
	checkResult, err := CheckRepository(ctx, checkedRepo, findings, true)
	require.NoError(t, err)
	assert.Empty(t, checkResult.Findings)
	assert.Equal(t, len(origPacks), checkResult.Packs)
	assert.Equal(t, 1, checkResult.Indexes)
}

func TestRepairIndexAbortsOnCorruptPack(t *testing.T) {
	ctx := context.Background()
	locDir := t.TempDir()
	loc := backend.NewLocal(locDir)
	repo, err := Init(ctx, loc, prunePassword)
	require.NoError(t, err)

	backupTestFiles(t, repo, map[string]string{
		"file1.txt": "valid pack content",
	}, []string{"site1"})

	origPacks := packHandles(t, loc)
	require.NotEmpty(t, origPacks)
	origIndexes := indexHandles(t, loc)
	require.NotEmpty(t, origIndexes)

	// Corrupt one pack file by overwriting its trailer bytes
	packPath := filepath.Join(locDir, "data", origPacks[0].Name[:2], origPacks[0].Name)
	f, err := os.OpenFile(packPath, os.O_WRONLY, 0o600)
	require.NoError(t, err)
	info, err := f.Stat()
	require.NoError(t, err)
	// Write garbage to the last 4 bytes (trailer)
	_, err = f.WriteAt([]byte{0xff, 0xff, 0xff, 0x7f}, info.Size()-4)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	repairRepo, err := OpenForRepair(ctx, loc, prunePassword)
	require.NoError(t, err)

	_, err = repairRepo.RepairIndex(ctx)
	require.Error(t, err)

	// Verify old indexes were preserved (not deleted)
	remainingIndexes := indexHandles(t, loc)
	assert.Equal(t, len(origIndexes), len(remainingIndexes))
}

func TestRepairIndexAbortsWhenHeaderLengthsDoNotFillPayload(t *testing.T) {
	ctx := context.Background()
	locDir := t.TempDir()
	loc := backend.NewLocal(locDir)
	repo, err := Init(ctx, loc, prunePassword)
	require.NoError(t, err)

	backupTestFiles(t, repo, map[string]string{
		"file1.txt": "valid pack content",
	}, []string{"site1"})

	origPacks := packHandles(t, loc)
	require.NotEmpty(t, origPacks)
	origIndexes := indexHandles(t, loc)
	require.NotEmpty(t, origIndexes)

	packPath := filepath.Join(locDir, "data", origPacks[0].Name[:2], origPacks[0].Name)
	packData, err := os.ReadFile(packPath)
	require.NoError(t, err)
	headerLength := int(binary.LittleEndian.Uint32(packData[len(packData)-4:]))
	headerOffset := len(packData) - 4 - headerLength
	header, err := repo.MasterKey().Open(nil, packData[headerOffset:len(packData)-4])
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(header), 5)
	binary.LittleEndian.PutUint32(header[1:5], binary.LittleEndian.Uint32(header[1:5])+1)
	sealedHeader, err := repo.MasterKey().Seal(nil, header)
	require.NoError(t, err)
	require.Len(t, sealedHeader, headerLength)
	copy(packData[headerOffset:len(packData)-4], sealedHeader)
	require.NoError(t, os.WriteFile(packPath, packData, 0o600))

	repairRepo, err := OpenForRepair(ctx, loc, prunePassword)
	require.NoError(t, err)
	_, err = repairRepo.RepairIndex(ctx)
	require.Error(t, err)

	// The failed validation must not replace the known-good index.
	assert.Len(t, indexHandles(t, loc), len(origIndexes))
}

func TestRepairIndexEmptyRepository(t *testing.T) {
	ctx := context.Background()
	loc := backend.NewLocal(t.TempDir())
	_, err := Init(ctx, loc, prunePassword)
	require.NoError(t, err)

	origIndexes := indexHandles(t, loc)
	require.Empty(t, origIndexes)

	repairRepo, err := OpenForRepair(ctx, loc, prunePassword)
	require.NoError(t, err)

	result, err := repairRepo.RepairIndex(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, result.PacksProcessed)
	assert.Equal(t, 0, result.BlobsIndexed)
	assert.Equal(t, 0, result.OldIndexesRemoved)
	assert.Equal(t, 1, result.NewIndexesWritten)

	newIndexes := indexHandles(t, loc)
	require.Len(t, newIndexes, 1)
}

func TestRepairIndexContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	loc := backend.NewLocal(t.TempDir())
	_, err := Init(context.Background(), loc, prunePassword)
	require.NoError(t, err)

	repairRepo, err := OpenForRepair(context.Background(), loc, prunePassword)
	require.NoError(t, err)

	_, err = repairRepo.RepairIndex(ctx)
	require.ErrorIs(t, err, context.Canceled)
}
