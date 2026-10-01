package summarize

import (
	"strings"
)

// material prefers parsed definitions over file count.
func material(n *SubtreeNode) int {
	if n == nil {
		return 0
	}
	if n.Material.Defs > 0 {
		return n.Material.Defs
	}
	return n.Material.SourceFiles
}

// applyDepthBound rolls up nodes below maxDepth.
func applyDepthBound(node *SubtreeNode, maxDepth int) {
	if node == nil || maxDepth <= 0 {
		return
	}
	applyDepthBoundAt(node, 0, maxDepth)
}

func applyDepthBoundAt(node *SubtreeNode, depth, maxDepth int) {
	if depth >= maxDepth {
		node.RollupOnly = true
		node.Children = nil
		return
	}
	for _, c := range node.Children {
		if c == nil {
			continue
		}
		applyDepthBoundAt(c, depth+1, maxDepth)
	}
}

// SumMaterialBottomUp aggregates directory material.
func SumMaterialBottomUp(node *SubtreeNode) {
	if node == nil {
		return
	}
	if node.Kind == SubtreeKindFile || len(node.Children) == 0 || node.LoadChildren != nil {
		return
	}
	var sum Material
	for _, c := range node.Children {
		if c == nil {
			continue
		}
		SumMaterialBottomUp(c)
		node.UnknownMaterial = node.UnknownMaterial || c.UnknownMaterial
		sum.Defs += c.Material.Defs
		sum.SourceFiles += c.Material.SourceFiles
		sum.Bytes += c.Material.Bytes
	}
	node.Material = sum
}

// FinalizeSubtree aggregates material and applies depth bounds.
func FinalizeSubtree(root *SubtreeNode, maxDepth int) *SubtreeNode {
	if root == nil {
		return nil
	}
	SumMaterialBottomUp(root)
	applyDepthBound(root, maxDepth)
	SumMaterialBottomUp(root)
	return root
}

// StructuralChild is one catalog tree row.
type StructuralChild struct {
	Path        string
	Kind        string // file | dir
	SourceFiles int
	Bytes       int64
	Defs        int
	ZoomIn      bool // depth-bound / map zoom — start as RollupOnly
	Children    []StructuralChild
}

// BuildSubtree converts catalog rows into a material tree.
func BuildSubtree(root StructuralChild, maxDepth int) *SubtreeNode {
	n := structuralToNode(root)
	return FinalizeSubtree(n, maxDepth)
}

func structuralToNode(s StructuralChild) *SubtreeNode {
	kind := s.Kind
	if kind == "" {
		if len(s.Children) > 0 {
			kind = SubtreeKindDir
		} else {
			kind = SubtreeKindFile
		}
	}
	n := &SubtreeNode{
		Path:       strings.TrimPrefix(s.Path, "./"),
		Kind:       kind,
		RollupOnly: s.ZoomIn,
		Material: Material{
			Defs:        s.Defs,
			SourceFiles: s.SourceFiles,
			Bytes:       s.Bytes,
		},
	}
	if n.Kind == SubtreeKindFile && n.Material.SourceFiles == 0 && !s.ZoomIn {
		n.Material.SourceFiles = 1
	}
	for _, c := range s.Children {
		n.Children = append(n.Children, structuralToNode(c))
	}
	return n
}

// CloneSubtree deep-copies a tree.
func CloneSubtree(n *SubtreeNode) *SubtreeNode {
	if n == nil {
		return nil
	}
	out := &SubtreeNode{
		CatalogRootID:   n.CatalogRootID,
		UnknownMaterial: n.UnknownMaterial,
		Representative:  n.Representative,
		ChildCount:      n.ChildCount,
		Remainder:       n.Remainder,
		LoadChildren:    n.LoadChildren,
		Path:            n.Path,
		Kind:            n.Kind,
		RollupOnly:      n.RollupOnly,
		ScopePaths:      append([]string(nil), n.ScopePaths...),
		Cursor:          n.Cursor,
		CursorScope:     n.CursorScope,
		Revision:        n.Revision,
		Material:        n.Material,
	}
	for _, c := range n.Children {
		out.Children = append(out.Children, CloneSubtree(c))
	}
	return out
}
