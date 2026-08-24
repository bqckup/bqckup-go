// Package tree implements directory trees: the Node metadata and the
// deterministic JSON serialization verified in
// restic-format-verification.md §2.8 ({"nodes":[...]} + trailing newline,
// strict byte-order sorting by name).
package tree

import (
	"encoding/json"
	"os"
	"time"

	"github.com/bqckup/bqckup-go/internal/engine/restic"
)

// Node types, exactly as restic spells them.
const (
	TypeFile      = "file"
	TypeDir       = "dir"
	TypeSymlink   = "symlink"
	TypeDev       = "dev"
	TypeCharDev   = "chardev"
	TypeFIFO      = "fifo"
	TypeSocket    = "socket"
	TypeIrregular = "irregular"
)

// ExtendedAttribute is a parsed xattr; we accept it when reading trees
// written by the real restic binary but do not produce it in L1.
type ExtendedAttribute struct {
	Name  string `json:"name"`
	Value []byte `json:"value"`
}

// Node is one filesystem object. Field names and tags match restic exactly
// so both readers accept both writers.
type Node struct {
	Name               string                     `json:"name"`
	Type               string                     `json:"type"`
	Mode               os.FileMode                `json:"mode,omitempty"`
	ModTime            time.Time                  `json:"mtime"`
	AccessTime         time.Time                  `json:"atime,omitempty"`
	ChangeTime         time.Time                  `json:"ctime,omitempty"`
	UID                uint32                     `json:"uid"`
	GID                uint32                     `json:"gid"`
	User               string                     `json:"user,omitempty"`
	Group              string                     `json:"group,omitempty"`
	Inode              uint64                     `json:"inode,omitempty"`
	DeviceID           uint64                     `json:"device_id,omitempty"`
	Size               uint64                     `json:"size,omitempty"`
	Links              uint64                     `json:"links,omitempty"`
	LinkTarget         string                     `json:"linktarget,omitempty"`
	LinkTargetRaw      string                     `json:"linktarget_raw,omitempty"`
	ExtendedAttributes []ExtendedAttribute        `json:"extended_attributes,omitempty"`
	GenericAttributes  map[string]json.RawMessage `json:"generic_attributes,omitempty"`
	Device             uint64                     `json:"device,omitempty"`
	Content            []restic.ID                `json:"content"`
	Subtree            *restic.ID                 `json:"subtree,omitempty"`
}

// Tree is one directory listing; nodes must be strictly sorted by name.
type Tree struct {
	Nodes []*Node `json:"nodes"`
}
