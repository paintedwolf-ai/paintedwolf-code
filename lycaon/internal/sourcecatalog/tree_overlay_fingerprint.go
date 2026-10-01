package sourcecatalog

import (
	"context"

	"github.com/lycaon/lycaon/internal/pagedview"
)

// FactFingerprint excludes derived visibility and weights from the retained facts.
func (o *TreeOverlay) FactFingerprint(ctx context.Context) (pagedview.Fingerprint, error) {
	depth, err := o.Depth(ctx)
	if err != nil {
		return pagedview.Fingerprint{}, err
	}
	var result pagedview.Fingerprint
	for level := 0; level <= depth; level++ {
		after := ""
		for {
			nodes, err := o.Level(ctx, level, after)
			if err != nil {
				return pagedview.Fingerprint{}, err
			}
			if len(nodes) == 0 {
				break
			}
			for _, node := range nodes {
				result = result.Combine(TreeRowFingerprint(node.Path, "review-fact", false, node.Directory, ""))
			}
			after = nodes[len(nodes)-1].Path
		}
	}
	return result, nil
}
