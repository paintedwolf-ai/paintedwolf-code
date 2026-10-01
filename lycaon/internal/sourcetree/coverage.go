package sourcetree

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

// Coverage follows disclosure exceptions over the catalog's subtree summaries.
func (p *Projection) unresolved(ctx context.Context, dir string) (int64, error) {
	node, err := p.reviewNode(ctx, dir)
	if err != nil || node.Visible && node.Virtual {
		return 0, err
	}
	rule, err := p.rule(ctx, dir)
	if err != nil || !rule.Open {
		return 0, err
	}
	state, err := p.Navigation.State(ctx, dir)
	if err != nil || state.Failure != "" {
		return 0, err
	}
	children, err := p.Navigation.Children(ctx, dir)
	if err != nil {
		return 0, err
	}
	pending := int64(0)
	if rule.Recursive {
		pending, err = children.Unresolved(ctx)
		if err != nil {
			return 0, err
		}
	}
	for _, child := range p.Rules.Branches(Address{Root: p.Root, Path: dir}) {
		entry, err := p.Navigation.Entry(ctx, child)
		if errors.Is(err, pagedview.ErrMissing) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if !entry.IsDir {
			continue
		}
		if rule.Recursive {
			item, _, err := children.Locate(ctx, sourcecatalog.DirectoryOrder(entry.Name, true))
			if err != nil {
				return 0, err
			}
			pending -= item.Unresolved
		}
		nested, err := p.unresolved(ctx, child)
		if err != nil {
			return 0, err
		}
		pending += nested
	}
	if !state.Complete && state.Failure == "" {
		pending++
	}
	return pending, err
}
