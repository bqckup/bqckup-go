package tree

import (
	"bytes"
	"crypto/sha256"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/engine/restic"
)

func fileNode(name string, size uint64, content ...restic.ID) *Node {
	return &Node{
		Name:    name,
		Type:    TypeFile,
		Mode:    0o644,
		ModTime: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC),
		UID:     1000,
		GID:     1000,
		Size:    size,
		Content: content,
	}
}

func dirNode(name string, subtree *restic.ID) *Node {
	return &Node{
		Name:    name,
		Type:    TypeDir,
		Mode:    0o755 | os.ModeDir,
		ModTime: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC),
		UID:     1000,
		GID:     1000,
		Subtree: subtree,
	}
}

func TestRoundTripPreservesFields(t *testing.T) {
	contentID := restic.Hash([]byte("file content"))
	subtreeID := restic.Hash([]byte("sub tree"))
	tr := &Tree{Nodes: []*Node{
		fileNode("b.txt", 12, contentID),
		{
			Name: "link", Type: TypeSymlink, Mode: 0o777 | os.ModeSymlink,
			ModTime: time.Date(2026, 8, 20, 10, 1, 0, 0, time.UTC),
			UID:     1000, GID: 1000, LinkTarget: "b.txt",
		},
		dirNode("sub", &subtreeID),
	}}
	doc, err := tr.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Unmarshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Nodes) != 3 {
		t.Fatalf("got %d nodes", len(restored.Nodes))
	}
	for i := range tr.Nodes {
		if !reflect.DeepEqual(tr.Nodes[i], restored.Nodes[i]) {
			t.Fatalf("node %d mismatch:\n%+v\n%+v", i, tr.Nodes[i], restored.Nodes[i])
		}
	}
}

func TestCanonicalBytesAndStableHash(t *testing.T) {
	// Same directory contents in different input orders must produce
	// identical bytes and therefore identical blob IDs.
	build := func(order []string) []byte {
		tr := &Tree{}
		for _, name := range order {
			tr.Nodes = append(tr.Nodes, fileNode(name, 1))
		}
		SortNodes(tr.Nodes)
		doc, err := tr.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	first := build([]string{"z.txt", "a.txt", "m.txt"})
	second := build([]string{"a.txt", "m.txt", "z.txt"})
	if !bytes.Equal(first, second) {
		t.Fatalf("same directory produced different bytes:\n%s\n%s", first, second)
	}
	// canonical form: {"nodes":[...]} + trailing newline
	if !bytes.HasPrefix(first, []byte(`{"nodes":[`)) || first[len(first)-1] != '\n' {
		t.Fatalf("not in canonical form: %s", first)
	}
	// stability: equal trees hash equal
	if sha256.Sum256(first) != sha256.Sum256(second) {
		t.Fatal("hashes differ for identical trees")
	}
}

func TestUnorderedTreeRejected(t *testing.T) {
	doc := []byte(`{"nodes":[{"name":"z","type":"file","mtime":"2026-08-20T10:00:00Z","atime":"2026-08-20T10:00:00Z","ctime":"2026-08-20T10:00:00Z","uid":0,"gid":0,"content":null},{"name":"a","type":"file","mtime":"2026-08-20T10:00:00Z","atime":"2026-08-20T10:00:00Z","ctime":"2026-08-20T10:00:00Z","uid":0,"gid":0,"content":null}]}`)
	if _, err := Unmarshal(doc); err == nil {
		t.Fatal("want error for unordered nodes")
	}
}

func TestFieldNamesMatchRestic(t *testing.T) {
	tr := &Tree{Nodes: []*Node{fileNode("f", 3, restic.Hash([]byte("x")))}}
	doc, err := tr.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		`"name":"f"`, `"type":"file"`, `"mode":420`, `"mtime"`,
		`"uid":1000`, `"gid":1000`, `"size":3`, `"content":`,
	} {
		if !bytes.Contains(doc, []byte(field)) {
			t.Fatalf("serialized tree misses %s: %s", field, doc)
		}
	}
	// like restic, atime/ctime are omitempty: absent unless the writer sets them
	withTimes := fileNode("f", 3, restic.Hash([]byte("x")))
	withTimes.AccessTime = withTimes.ModTime
	withTimes.ChangeTime = withTimes.ModTime
	withTimesDoc, err := (&Tree{Nodes: []*Node{withTimes}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"atime"`, `"ctime"`} {
		if !bytes.Contains(withTimesDoc, []byte(field)) {
			t.Fatalf("serialized tree misses %s: %s", field, withTimesDoc)
		}
	}
	// a restic-shaped node with xattrs must parse
	upstream := []byte(`{"nodes":[{"name":"f","type":"file","mode":420,"mtime":"2026-08-20T10:00:00Z","atime":"2026-08-20T10:00:00Z","ctime":"2026-08-20T10:00:00Z","uid":1000,"gid":1000,"inode":42,"device_id":7,"size":3,"links":1,"extended_attributes":[{"name":"user.tag","value":"dmFsdWU="}],"generic_attributes":{"x":1},"content":["c3ab8ff13720e8ad9047dd39466b3c8974e592c2fa383d4a3960714caef0c4f2"]}]}`)
	if _, err := Unmarshal(upstream); err != nil {
		t.Fatalf("upstream-shaped tree failed to parse: %v", err)
	}
}
