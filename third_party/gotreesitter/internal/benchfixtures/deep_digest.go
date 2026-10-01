package benchfixtures

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"unicode/utf8"
)

// DeepTreeDigestVersion names the byte-stable structural digest contract.
const DeepTreeDigestVersion = "gts-deep-tree-v1"

// DeepNode is one node in a preorder, child-order-preserving tree stream.
// Field is the field name on the edge from the parent; it is empty for the root.
type DeepNode struct {
	Type        string
	Field       string
	StartByte   uint32
	EndByte     uint32
	StartRow    uint32
	StartColumn uint32
	EndRow      uint32
	EndColumn   uint32
	Named       bool
	Extra       bool
	Missing     bool
	Error       bool
	HasError    bool
	ChildCount  uint32
}

// NodeKindCoverage records visible tree node kinds observed during an untimed
// deep-tree admission walk.
type NodeKindCoverage map[string]uint64

// Merge adds another coverage census into c.
func (c NodeKindCoverage) Merge(other NodeKindCoverage) {
	for kind, count := range other {
		c[kind] += count
	}
}

// DeepTreeInspection pairs the authenticated tree digest with the node kinds
// observed while computing it.
type DeepTreeInspection struct {
	SHA256    string
	NodeKinds NodeKindCoverage
}

// DeepTreeDigest incrementally computes SHA-256 over an authenticated preorder
// node stream. NewDeepTreeDigest writes the ASCII version string followed by a
// NUL byte. Add then writes, for every node:
//
//   - uint32 little-endian type byte length, then UTF-8 type bytes
//   - uint32 little-endian field byte length, then UTF-8 field bytes
//   - start byte, end byte, start row, start column, end row, end column
//     as six uint32 little-endian values
//   - one flag byte: named=bit 0, extra=bit 1, missing=bit 2,
//     is-error=bit 3, has-error=bit 4
//   - uint32 little-endian child count
//
// Type plus the named flag is the cross-runtime visible symbol identity.
// Numeric grammar symbol IDs are intentionally absent: the generated Go blob
// and pinned C grammar assign different IDs to some identical visible symbols.
// Nodes must be added in preorder with children in parser order. Child counts
// make the stream self-delimiting and are validated before Hex succeeds.
type DeepTreeDigest struct {
	h         hash.Hash
	open      uint64
	nodes     uint64
	complete  bool
	nodeKinds NodeKindCoverage
}

// NewDeepTreeDigest starts an empty v1 digest.
func NewDeepTreeDigest() *DeepTreeDigest {
	h := sha256.New()
	_, _ = h.Write([]byte(DeepTreeDigestVersion))
	_, _ = h.Write([]byte{0})
	return &DeepTreeDigest{h: h, open: 1, nodeKinds: make(NodeKindCoverage)}
}

// Add appends one preorder node and validates the tree framing seen so far.
func (d *DeepTreeDigest) Add(node DeepNode) error {
	if d == nil || d.h == nil {
		return fmt.Errorf("deep tree digest is uninitialized")
	}
	if d.complete || d.open == 0 {
		return fmt.Errorf("deep tree digest has an extra node after the tree closed")
	}
	if !utf8.ValidString(node.Type) || !utf8.ValidString(node.Field) {
		return fmt.Errorf("deep tree digest node contains invalid UTF-8")
	}
	if node.StartByte > node.EndByte {
		return fmt.Errorf("deep tree digest node has reversed byte range %d..%d", node.StartByte, node.EndByte)
	}

	d.open--
	d.open += uint64(node.ChildCount)
	d.nodes++
	d.nodeKinds[node.Type]++
	d.writeString(node.Type)
	d.writeString(node.Field)
	d.writeUint32(node.StartByte)
	d.writeUint32(node.EndByte)
	d.writeUint32(node.StartRow)
	d.writeUint32(node.StartColumn)
	d.writeUint32(node.EndRow)
	d.writeUint32(node.EndColumn)
	var flags byte
	if node.Named {
		flags |= 1 << 0
	}
	if node.Extra {
		flags |= 1 << 1
	}
	if node.Missing {
		flags |= 1 << 2
	}
	if node.Error {
		flags |= 1 << 3
	}
	if node.HasError {
		flags |= 1 << 4
	}
	_, _ = d.h.Write([]byte{flags})
	d.writeUint32(node.ChildCount)
	if d.open == 0 {
		d.complete = true
	}
	return nil
}

// Hex returns the lowercase SHA-256 digest for one complete rooted tree.
func (d *DeepTreeDigest) Hex() (string, error) {
	if d == nil || d.h == nil || d.nodes == 0 {
		return "", fmt.Errorf("deep tree digest is empty")
	}
	if !d.complete || d.open != 0 {
		return "", fmt.Errorf("deep tree digest is incomplete: %d child nodes remain", d.open)
	}
	return fmt.Sprintf("%x", d.h.Sum(nil)), nil
}

// Inspection returns the completed digest and a defensive copy of its node
// kind census.
func (d *DeepTreeDigest) Inspection() (DeepTreeInspection, error) {
	sha256, err := d.Hex()
	if err != nil {
		return DeepTreeInspection{}, err
	}
	kinds := make(NodeKindCoverage, len(d.nodeKinds))
	kinds.Merge(d.nodeKinds)
	return DeepTreeInspection{SHA256: sha256, NodeKinds: kinds}, nil
}

func (d *DeepTreeDigest) writeString(value string) {
	d.writeUint32(uint32(len(value)))
	_, _ = d.h.Write([]byte(value))
}

func (d *DeepTreeDigest) writeUint32(value uint32) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], value)
	_, _ = d.h.Write(buf[:])
}
