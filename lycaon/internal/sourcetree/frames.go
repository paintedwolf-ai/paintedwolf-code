package sourcetree

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

type Frame struct {
	pagedview.Frame[Row, Address]
	Ancestors              []Ancestor
	ResolvedAnchor         Address
	Target                 *int64
	Prefix, RetainedPrefix *FramePrefix
}
type FrameRequest struct {
	Offset int64
	Limit  int
	Anchor *Address
	Before int
	Retain []FramePrefix
}
type rootProjection struct {
	root           Root
	projection     Projection
	rows           int64
	releaseReview  func()
	reviewRevision string
}

// Each root is read at one retained generation.
type snapshot struct {
	revision      pagedview.Revision
	roots         []rootProjection
	complete      bool
	releaseReview func()
}

func (v *View) snapshot(ctx context.Context) (*snapshot, error) {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return nil, pagedview.ErrExpired
	}
	rules := v.rules.Clone()
	review := v.review
	frame := &snapshot{revision: pagedview.Revision{Intent: v.revision}, complete: true}
	if review != nil {
		frame.releaseReview = review.retain()
	}

	v.mu.Unlock()
	var identity strings.Builder
	identity.WriteString(frame.revision.Intent)
	roots := append([]Root(nil), v.rootOrder...)
	// Acquire bounded root pools in one order across views before restoring display order.
	sort.Slice(roots, func(i, j int) bool {
		if roots[i].Path == roots[j].Path {
			return roots[i].ID < roots[j].ID
		}
		return roots[i].Path < roots[j].Path
	})
	open := func(ctx context.Context, root Root) (*sourcecatalog.Navigation, error) {
		return v.openPreparedNavigation(ctx, root.Root, &rules)
	}

	for _, root := range roots {
		prepared := rootProjection{root: root, projection: Projection{Root: root.ID, Rules: &rules}}
		if review != nil && review.hasFacts(root.ID) {
			navigation, err := open(ctx, root)
			if err != nil {
				frame.Close()
				return nil, err
			}
			basis := Projection{Root: root.ID, Rules: &rules, Navigation: navigation}
			prepared.reviewRevision, err = basis.revision(ctx)
			if err != nil {
				_ = navigation.Close()
				frame.Close()
				return nil, err
			}
			pin, err := navigation.Retain()
			_ = navigation.Close()
			if err != nil {
				frame.Close()
				return nil, err
			}
			source := reviewSource{root: root.ID, rules: &rules, revision: prepared.reviewRevision, pin: pin, open: func(ctx context.Context) (*sourcecatalog.Navigation, error) {
				return pin.OpenNavigation(ctx)
			}}
			var errBuild error
			//nolint:contextcheck // Shared preparation follows the view lifetime, independently of this frame.
			prepared.projection.Review, prepared.releaseReview, errBuild = review.projection(v.ctx, source, frame.revision.Intent, v.notify)
			if errBuild != nil {
				frame.Close()
				return nil, errBuild
			}
		}
		frame.roots = append(frame.roots, prepared)
	}
	for i := range frame.roots {
		root := &frame.roots[i]
		navigation, err := open(ctx, root.root)
		if err != nil {
			frame.Close()
			return nil, err
		}
		root.projection.Navigation = navigation
		projectionRevision, err := root.projection.revision(ctx)
		if err != nil {
			frame.Close()
			return nil, err
		}
		if root.projection.Review != nil && projectionRevision != root.reviewRevision {
			frame.Close()
			return nil, pagedview.ErrRevision
		}
		root.rows, err = root.projection.Count(ctx)
		if err != nil {
			frame.Close()
			return nil, err
		}
		pending, err := root.projection.unresolved(ctx, ".")
		if err != nil {
			frame.Close()
			return nil, err
		}
		frame.complete = frame.complete && pending == 0
		identity.WriteByte(0)
		identity.WriteString(root.root.ID)
		identity.WriteByte(0)
		identity.WriteString(projectionRevision)
	}
	positions := make(map[string]int, len(v.rootOrder))
	for i, root := range v.rootOrder {
		positions[root.ID] = i
	}
	sort.Slice(frame.roots, func(i, j int) bool { return positions[frame.roots[i].root.ID] < positions[frame.roots[j].root.ID] })
	digest := sha256.Sum256([]byte(identity.String()))
	frame.revision.Projection = hex.EncodeToString(digest[:])

	return frame, nil
}
func (s *snapshot) Close() {
	if s.releaseReview != nil {
		defer s.releaseReview()
	}
	for _, root := range s.roots {
		if root.projection.Navigation != nil {
			_ = root.projection.Navigation.Close()
		}
		if root.releaseReview != nil {
			root.releaseReview()
		}
	}
}
func (s *snapshot) total() int64 {
	var total int64
	for _, root := range s.roots {
		total += root.rows
	}
	return total
}
func (s *snapshot) locate(ctx context.Context, address Address) (Location, error) {
	if !validAddress(address) {
		return Location{}, ErrAddress
	}
	offset := int64(0)
	for _, root := range s.roots {
		if root.root.ID == address.Root {
			location, err := root.projection.Locate(ctx, address.Path)
			location.Index += offset
			return location, err
		}
		offset += root.rows
	}
	return Location{}, ErrUnknownRoot
}

func (snapshot *snapshot) frame(ctx context.Context, request FrameRequest) (Frame, error) {
	var err error
	offset := request.Offset
	before := request.Before
	var resolved Address
	var targetIndex *int64
	if request.Anchor != nil {
		location, err := snapshot.locate(ctx, *request.Anchor)
		if err != nil {
			return Frame{}, err
		}
		target, err := pagedview.AnchorOffset(location.Index, request.Offset, snapshot.total(), 0, true)
		if err != nil {
			return Frame{}, err
		}
		targetIndex = &target
		offset = max(0, target-int64(before))
		resolved = location.Address
	}
	if offset > snapshot.total() {
		return Frame{}, pagedview.ErrRange
	}
	frame := Frame{Frame: pagedview.Frame[Row, Address]{Revision: snapshot.revision, Extent: pagedview.Extent{Rows: snapshot.total(), Complete: snapshot.complete}, Span: pagedview.Span{Start: offset}, Rows: []Row{}}}
	frame.ResolvedAnchor, frame.Target = resolved, targetIndex
	base := int64(0)
	for _, root := range snapshot.roots {
		if offset >= base+root.rows {
			base += root.rows
			continue
		}
		start := max(0, offset-base)
		rows, err := root.projection.frameRows(ctx, start, request.Limit-len(frame.Rows), root.rows)
		if err != nil {
			return Frame{}, err
		}
		if len(rows) > 0 && len(frame.Rows) == 0 {
			frame.Anchor = rows[0].Address
			frame.Ancestors, err = root.projection.Ancestors(ctx, rows[0])
			if err != nil {
				return Frame{}, err
			}
			for i := range frame.Ancestors {
				ancestor := &frame.Ancestors[i]
				ancestor.Index += base
				ancestor.End += base
				if ancestor.Address.Path == "." {
					ancestor.Name = root.root.Label
				}
			}
		}
		for i := range rows {
			if rows[i].Address.Path == "." && rows[i].Kind == "directory" {
				rows[i].Name = root.root.Label
			}
		}
		frame.Rows = append(frame.Rows, rows...)
		if len(frame.Rows) >= request.Limit {
			break
		}
		base += root.rows
	}
	frame.Span.End = offset + int64(len(frame.Rows))
	err = snapshot.retainFrame(ctx, &frame, request.Retain)
	return frame, err
}

func (v *View) Revision(ctx context.Context) (pagedview.Revision, pagedview.Extent, error) {
	presentation, err := v.Capture(ctx)
	if err != nil {
		return pagedview.Revision{}, pagedview.Extent{}, err
	}
	defer presentation.Close()
	revision, extent := presentation.Revision()
	return revision, extent, nil
}
