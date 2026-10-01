package sourcetree

import (
	"context"
	"sort"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

type presentationRoot struct {
	root          Root
	rules         *Rules
	rows          int64
	pin           *sourcecatalog.GenerationPin
	review        *ReviewProjection
	releaseReview func()
}

// Presentation retains complete coordinates independently of subsequent intent.
type Presentation struct {
	refs     atomic.Int64
	revision pagedview.Revision
	extent   pagedview.Extent
	roots    []presentationRoot
}

func (v *View) Capture(ctx context.Context) (*Presentation, error) {
	v.mu.Lock()
	preparationError := v.prepareError
	intent := v.revision
	v.mu.Unlock()
	frame, err := v.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer frame.Close()
	if !frame.complete {
		if preparationError != nil {
			return nil, &PreparationError{Err: preparationError}
		}
		v.Prepare()
		return nil, pagedview.ErrPreparing
	}
	result := &Presentation{revision: frame.revision, extent: pagedview.Extent{Rows: frame.total(), Complete: true}}
	result.refs.Store(1)
	for i := range frame.roots {
		root := &frame.roots[i]
		pin, err := root.projection.Navigation.Retain()
		if err != nil {
			result.Close()
			return nil, err
		}
		result.roots = append(result.roots, presentationRoot{root: root.root, rules: root.projection.Rules,
			rows: root.rows, pin: pin, review: root.projection.Review, releaseReview: root.releaseReview})
		root.releaseReview = nil
	}
	v.mu.Lock()
	current := !v.closed && v.revision == intent
	v.mu.Unlock()
	if !current {
		result.Close()
		return nil, pagedview.ErrRevision
	}
	return result, nil
}

func (p *Presentation) Revision() (pagedview.Revision, pagedview.Extent) { return p.revision, p.extent }
func (p *Presentation) Retain()                                          { p.refs.Add(1) }
func (p *Presentation) Close() {
	if p.refs.Add(-1) != 0 {
		return
	}
	for _, root := range p.roots {
		root.pin.Release()
		if root.releaseReview != nil {
			root.releaseReview()
		}
	}
}

func (p *Presentation) snapshot(ctx context.Context) (*snapshot, error) {
	frame := &snapshot{revision: p.revision, complete: true}
	roots := append([]presentationRoot(nil), p.roots...)
	sort.Slice(roots, func(i, j int) bool {
		if roots[i].root.Path == roots[j].root.Path {
			return roots[i].root.ID < roots[j].root.ID
		}
		return roots[i].root.Path < roots[j].root.Path
	})
	for _, root := range roots {
		nav, err := root.pin.OpenNavigation(ctx)
		if err != nil {
			frame.Close()
			return nil, err
		}
		projection := Projection{Root: root.root.ID, Rules: root.rules, Navigation: nav,
			baseCache: pagedview.NewCache[string, projectedChildren](128, 2<<20)}
		if root.review != nil {
			projection.Review = root.review.clone()
		}
		frame.roots = append(frame.roots, rootProjection{root: root.root, rows: root.rows, projection: projection})
	}
	positions := make(map[string]int, len(p.roots))
	for i, root := range p.roots {
		positions[root.root.ID] = i
	}
	sort.Slice(frame.roots, func(i, j int) bool { return positions[frame.roots[i].root.ID] < positions[frame.roots[j].root.ID] })
	return frame, nil
}

func (p *Presentation) Frame(ctx context.Context, request FrameRequest) (Frame, error) {
	if request.Offset < 0 || request.Limit < 1 || request.Limit > pagedview.MaxRows || request.Before < 0 || request.Before >= request.Limit || request.Before > 0 && request.Anchor == nil || len(request.Retain) > 32 {
		return Frame{}, pagedview.ErrRange
	}
	frame, err := p.snapshot(ctx)
	if err != nil {
		return Frame{}, err
	}
	defer frame.Close()
	return frame.frame(ctx, request)
}

func (p *Presentation) Locate(ctx context.Context, address Address) (Location, pagedview.Revision, error) {
	frame, err := p.snapshot(ctx)
	if err != nil {
		return Location{}, pagedview.Revision{}, err
	}
	defer frame.Close()
	location, err := frame.locate(ctx, address)
	return location, p.revision, err
}

// Bytes includes the generation each root pins. A presentation that outlives
// its generation holds segments no other reader names, and the retention budget
// has to see them to evict it.
func (p *Presentation) Bytes() int64 {
	total := int64(256 + len(p.roots)*256)
	if len(p.roots) > 0 {
		total += p.roots[0].rules.bytes()
	}
	for _, root := range p.roots {
		total += root.pin.RetainedBytes()
	}
	return total
}
