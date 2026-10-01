package sourcecatalog

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/pagedview"
)

type cachedOverlayNode struct {
	node    OverlayNode
	missing bool
}

// TreeOverlayReader bounds decoded detail for one immutable overlay read.
type TreeOverlayReader struct {
	overlay  *TreeOverlay
	nodes    *pagedview.Cache[string, cachedOverlayNode]
	children *pagedview.Cache[string, *pagedview.RangeIndex[TreeItem]]
	pages    *pagedview.Cache[uint64, pagedview.RangePage[TreeItem]]
}

func (o *TreeOverlay) Reader() *TreeOverlayReader {
	return &TreeOverlayReader{overlay: o, nodes: pagedview.NewCache[string, cachedOverlayNode](512, 256<<10), children: pagedview.NewCache[string, *pagedview.RangeIndex[TreeItem]](256, 128<<10), pages: pagedview.NewCache[uint64, pagedview.RangePage[TreeItem]](128, 2<<20)}
}
func (r *TreeOverlayReader) Node(ctx context.Context, rel string) (OverlayNode, error) {
	if cached, ok := r.nodes.Get(rel); ok {
		if cached.missing {
			return OverlayNode{}, pagedview.ErrMissing
		}
		return cached.node, nil
	}
	node, err := r.overlay.Node(ctx, rel)
	if err == nil || errors.Is(err, pagedview.ErrMissing) {
		r.nodes.Put(rel, cachedOverlayNode{node: node, missing: err != nil}, int64(192+len(rel)+len(node.Path)+len(node.Parent)))
	}
	return node, err
}
func (r *TreeOverlayReader) Children(ctx context.Context, dir string) (*pagedview.RangeIndex[TreeItem], error) {
	if index, ok := r.children.Get(dir); ok {
		return index, nil
	}
	index, _, err := newRangeReader(r.overlay.rows.db, r.pages).Open(ctx, r.overlay.rows.id, dir)
	if err == nil {
		r.children.Put(dir, index, int64(128+len(dir)))
	}
	return index, err
}
