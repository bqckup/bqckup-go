package snapshot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/engine/incremental"
)

func TestRoundTripPreservesFields(t *testing.T) {
	treeID := incremental.Hash([]byte("root tree"))
	parentID := incremental.Hash([]byte("parent"))
	originalID := incremental.Hash([]byte("original"))
	snap := &Snapshot{
		Time:           time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
		Parent:         &parentID,
		Tree:           &treeID,
		Paths:          []string{"/srv/example"},
		Hostname:       "backup-host",
		Username:       "backup-user",
		UID:            1000,
		GID:            1000,
		Excludes:       []string{"/srv/example/cache"},
		Tags:           []string{"bqckup", "site:example"},
		Original:       &originalID,
		ProgramVersion: "bqckup 0.1.0",
		Summary: &Summary{
			FilesNew: 3, FilesChanged: 1, FilesUnmodified: 2,
			DataBlobs: 10, TreeBlobs: 4, DataAdded: 12345,
			TotalFilesProcessed: 6, TotalBytesProcessed: 100000,
			TotalDuration: 1.25, SnapshotID: "abc123",
		},
	}
	doc, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var restored Snapshot
	if err := json.Unmarshal(doc, &restored); err != nil {
		t.Fatal(err)
	}
	if *restored.Tree != treeID || restored.Parent == nil || *restored.Parent != parentID {
		t.Fatal("id fields lost in round trip")
	}
	if restored.Time != snap.Time || restored.UID != snap.UID || restored.GID != snap.GID {
		t.Fatal("scalar fields lost in round trip")
	}
	if len(restored.Paths) != 1 || restored.Paths[0] != "/srv/example" {
		t.Fatal("paths lost in round trip")
	}
	if len(restored.Tags) != 2 || restored.Tags[1] != "site:example" {
		t.Fatal("tags lost in round trip")
	}
	if restored.Summary == nil || restored.Summary.FilesNew != 3 || restored.Summary.DataAdded != 12345 {
		t.Fatal("summary lost in round trip")
	}
}

func TestOptionalFieldsOmitted(t *testing.T) {
	treeID := incremental.Hash([]byte("root tree"))
	snap := &Snapshot{Time: time.Now().UTC(), Tree: &treeID, Paths: []string{"/srv"}}
	doc, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	text := string(doc)
	for _, absent := range []string{"parent", "hostname", "username", "tags", "original", "summary", "program_version"} {
		if jsonKeyPresent(text, absent) {
			t.Fatalf("optional field %q should be omitted: %s", absent, doc)
		}
	}
	if !jsonKeyPresent(text, "tree") || !jsonKeyPresent(text, "time") {
		t.Fatalf("required fields missing: %s", doc)
	}
}

func TestFieldNamesMatchRestic(t *testing.T) {
	treeID := incremental.Hash([]byte("root tree"))
	snap := &Snapshot{
		Time:  time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
		Tree:  &treeID,
		Paths: []string{"/srv/example"},
		Tags:  []string{"bqckup"},
	}
	doc, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`time`, `tree`, `paths`, `tags`} {
		if !jsonKeyPresent(string(doc), field) {
			t.Fatalf("snapshot misses %s: %s", field, doc)
		}
	}
	// a restic-shaped snapshot must parse (fields we do not model are dropped)
	upstream := []byte(`{"time":"2026-08-20T12:00:00Z","parent":"c3ab8ff13720e8ad9047dd39466b3c8974e592c2fa383d4a3960714caef0c4f2","tree":"3ec79977ef0cf5de7b08cd12b874cd0f62bbaf7f07f3497a5b1bbcc8cb39b1ce","paths":["/srv"],"hostname":"h","username":"u","uid":1000,"gid":1000,"excludes":["*.tmp"],"tags":["t"],"original":"3ec79977ef0cf5de7b08cd12b874cd0f62bbaf7f07f3497a5b1bbcc8cb39b1ce","program_version":"restic 0.19.0","summary":{"files_new":1,"data_blobs":2,"tree_blobs":3,"data_added":99,"total_files_processed":1,"total_bytes_processed":77,"total_duration":0.5,"snapshot_id":"s","some_future_field":true}}`)
	var parsed Snapshot
	if err := json.Unmarshal(upstream, &parsed); err != nil {
		t.Fatalf("upstream snapshot failed to parse: %v", err)
	}
	if parsed.Tree == nil || parsed.Parent == nil || parsed.Summary == nil || parsed.Summary.DataBlobs != 2 {
		t.Fatalf("unexpected parse result: %+v", parsed)
	}
}

func jsonKeyPresent(doc, key string) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(doc), &probe); err != nil {
		return false
	}
	_, ok := probe[key]
	return ok
}
