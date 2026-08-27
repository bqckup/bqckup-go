package tree

import (
	"encoding/json"
	"errors"
	"fmt"
)

// errNotOrdered mirrors restic's ErrTreeNotOrdered.
var errNotOrdered = errors.New("tree: nodes are not sorted by name")

// Add appends a node, enforcing strict byte-order sorting by name like
// restic does (its reader rejects unordered trees).
func (t *Tree) Add(node *Node) error {
	if len(t.Nodes) > 0 {
		last := t.Nodes[len(t.Nodes)-1].Name
		if node.Name <= last {
			return fmt.Errorf("%w: %q after %q", errNotOrdered, node.Name, last)
		}
	}
	t.Nodes = append(t.Nodes, node)
	return nil
}

// Marshal returns the canonical tree bytes: {"nodes":[...]} + trailing
// newline. Deterministic: sorted nodes, fixed struct field order.
func (t *Tree) Marshal() ([]byte, error) {
	doc, err := json.Marshal(t)
	if err != nil {
		return nil, fmt.Errorf("tree: marshal: %w", err)
	}
	return append(doc, '\n'), nil
}

// Unmarshal parses tree bytes and verifies node order.
func Unmarshal(data []byte) (*Tree, error) {
	var t Tree
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("tree: unmarshal: %w", err)
	}
	for i := 1; i < len(t.Nodes); i++ {
		if t.Nodes[i-1].Name >= t.Nodes[i].Name {
			return nil, fmt.Errorf("%w: %q then %q", errNotOrdered, t.Nodes[i-1].Name, t.Nodes[i].Name)
		}
	}
	return &t, nil
}
