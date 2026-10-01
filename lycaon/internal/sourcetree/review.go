package sourcetree

import (
	"context"
	"errors"
	"path"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

// ReviewProjection adds only missing review paths to shared physical directory pages.
// Each reader has bounded lookup caches; the published overlay is immutable.
type ReviewProjection struct {
	reader  *sourcecatalog.TreeOverlayReader
	overlay *sourcecatalog.TreeOverlay
}

func NewReviewProjection(overlay *sourcecatalog.TreeOverlay) *ReviewProjection {
	return &ReviewProjection{reader: overlay.Reader(), overlay: overlay}
}
func (r *ReviewProjection) node(ctx context.Context, rel string) (sourcecatalog.OverlayNode, error) {
	return r.reader.Node(ctx, rel)
}
func (r *ReviewProjection) index(ctx context.Context, dir string) (*pagedview.RangeIndex[sourcecatalog.TreeItem], error) {
	return r.reader.Children(ctx, dir)
}

// reviewSource reads one retained generation for the whole build; pin keeps it.
type reviewSource struct {
	root     string
	rules    *Rules
	revision string
	pin      *sourcecatalog.GenerationPin
	open     func(context.Context) (*sourcecatalog.Navigation, error)
}

// prepareReview reads at most one sparse page per snapshot and releases it before writes.
func prepareReview(ctx context.Context, source reviewSource, overlay *sourcecatalog.TreeOverlay) error {
	depth, err := overlay.Depth(ctx)
	if err != nil {
		return err
	}
	for level := 0; level <= depth; level++ {
		if err := reviewLevel(ctx, source, overlay, level, false); err != nil {
			return err
		}
	}
	for level := depth; level >= 0; level-- {
		if err := reviewLevel(ctx, source, overlay, level, true); err != nil {
			return err
		}
	}
	navigation, err := source.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = navigation.Close() }()
	base := Projection{Root: source.root, Rules: source.rules, Navigation: navigation}
	revision, err := base.revision(ctx)
	if err != nil {
		return err
	}
	if revision != source.revision {
		return pagedview.ErrRevision
	}
	return nil
}
func reviewLevel(ctx context.Context, source reviewSource, overlay *sourcecatalog.TreeOverlay, level int, weights bool) error {
	after := ""
	for {
		nodes, err := overlay.Level(ctx, level, after)
		if err != nil || len(nodes) == 0 {
			return err
		}
		if err := classifyReviewPage(ctx, source, overlay, nodes, weights); err != nil {
			return err
		}
		if weights {
			err = overlay.Weigh(ctx, nodes)
		} else {
			err = overlay.Classify(ctx, nodes)
		}
		if err != nil {
			return err
		}
		after = nodes[len(nodes)-1].Path
	}
}
func classifyReviewPage(ctx context.Context, source reviewSource, overlay *sourcecatalog.TreeOverlay, nodes []sourcecatalog.OverlayNode, weights bool) error {
	navigation, err := source.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = navigation.Close() }()
	base := &Projection{Root: source.root, Rules: source.rules, Navigation: navigation}
	revision, err := base.revision(ctx)
	if err != nil {
		return err
	}
	if revision != source.revision {
		return pagedview.ErrRevision
	}
	for i := range nodes {
		if weights {
			err = weighReviewNode(ctx, base, overlay, &nodes[i])
		} else {
			err = classifyReviewNode(ctx, base, overlay, &nodes[i])
		}
		if err != nil {
			return err
		}
	}
	return nil
}
func classifyReviewNode(ctx context.Context, base *Projection, overlay *sourcecatalog.TreeOverlay, node *sourcecatalog.OverlayNode) error {
	if node.Path == "." {
		node.Visible = true
		return nil
	}
	parent, err := overlay.Node(ctx, node.Parent)
	if err != nil {
		return err
	}
	if !parent.Visible {
		return nil
	}
	open := base.Rules.atDefault(Address{Root: base.Root, Path: node.Parent}, true).Open
	if !parent.Virtual {
		open, err = base.open(ctx, node.Parent)
		if err != nil {
			return err
		}
	}
	if !open {
		return nil
	}
	if parent.Virtual {
		node.Visible, node.Virtual = true, true
		return nil
	}
	state, err := base.Navigation.State(ctx, node.Parent)
	if err != nil || !state.Complete {
		return err
	}
	entry, err := base.Navigation.Entry(ctx, node.Path)
	if errors.Is(err, pagedview.ErrMissing) {
		node.Visible, node.Virtual = true, true
		return nil
	}
	if err != nil {
		return err
	}
	node.Visible = entry.IsDir && node.Directory
	return nil
}
func weighReviewNode(ctx context.Context, base *Projection, overlay *sourcecatalog.TreeOverlay, node *sourcecatalog.OverlayNode) error {
	if !node.Visible {
		return nil
	}
	if node.Virtual {
		node.Weight = 1
	}
	if !node.Directory {
		return nil
	}
	var open bool
	var err error
	if node.Virtual {
		open = base.Rules.atDefault(Address{Root: base.Root, Path: node.Path}, true).Open
	} else {
		open, err = base.open(ctx, node.Path)
	}
	if err != nil || !open {
		return err
	}
	index, err := overlay.Children(ctx, node.Path)
	if err != nil {
		return err
	}
	extra, err := index.Extent(ctx)
	if err != nil {
		return err
	}
	if !node.Virtual && extra > 0 {
		state, err := base.Navigation.State(ctx, node.Path)
		if err != nil {
			return err
		}
		children, err := base.Navigation.Children(ctx, node.Path)
		if err != nil {
			return err
		}
		count, err := children.Count(ctx)
		if err != nil {
			return err
		}
		node.SuppressEmpty = sourcecatalog.DirectoryAncillary(state, count) == "empty"
		if node.SuppressEmpty {
			extra--
		}
	}
	node.Weight += extra
	return nil
}

func (p *Projection) reviewNode(ctx context.Context, rel string) (sourcecatalog.OverlayNode, error) {
	if p.Review == nil {
		return sourcecatalog.OverlayNode{}, nil
	}
	node, err := p.Review.node(ctx, rel)
	if errors.Is(err, pagedview.ErrMissing) {
		return sourcecatalog.OverlayNode{}, nil
	}
	return node, err
}
func (p *Projection) entry(ctx context.Context, rel string) (sourcecatalog.Entry, error) {
	node, err := p.reviewNode(ctx, rel)
	if err != nil {
		return sourcecatalog.Entry{}, err
	}
	if node.Visible && node.Virtual {
		return sourcecatalog.Entry{RootID: p.Root, Path: rel, Parent: node.Parent, Name: path.Base(rel), Depth: node.Depth, IsDir: node.Directory}, nil
	}
	return p.Navigation.Entry(ctx, rel)
}
func (p *Projection) ancillary(ctx context.Context, dir string) (string, string, error) {
	node, err := p.reviewNode(ctx, dir)
	if err != nil {
		return "", "", err
	}
	if node.Visible && (node.Virtual || node.SuppressEmpty) {
		return "", "", nil
	}
	state, err := p.Navigation.State(ctx, dir)
	if err != nil {
		return "", "", err
	}
	index, err := p.Navigation.Children(ctx, dir)
	if err != nil {
		return "", "", err
	}
	count, err := index.Count(ctx)
	return sourcecatalog.DirectoryAncillary(state, count), state.Failure, err
}

func (p *Projection) children(ctx context.Context, dir string) (int64, []replacement, error) {
	total, replacements, err := p.baseChildren(ctx, dir)
	if err != nil || p.Review == nil {
		return total, replacements, err
	}
	open, err := p.open(ctx, dir)
	if err != nil || !open {
		return total, replacements, err
	}
	index, err := p.Review.index(ctx, dir)
	if err != nil {
		return 0, nil, err
	}
	added, err := index.Extent(ctx)
	if err != nil {
		return 0, nil, err
	}
	node, err := p.reviewNode(ctx, dir)
	if err != nil {
		return 0, nil, err
	}
	if node.SuppressEmpty {
		added--
	}
	return total + added, replacements, nil
}

func (p *Projection) child(ctx context.Context, dir string, rank int64) (sourcecatalog.Entry, int64, error) {
	if p.Review == nil {
		return p.baseChild(ctx, dir, rank)
	}
	merged, err := p.reviewChildren(ctx, dir)
	if err != nil {
		return sourcecatalog.Entry{}, 0, err
	}
	item, offset, err := merged.Select(ctx, rank)
	if err != nil {
		return sourcecatalog.Entry{}, 0, err
	}
	entry, err := p.entry(ctx, item.Value.Path)
	return entry, offset, err
}

func (p *Projection) rank(ctx context.Context, dir string, entry sourcecatalog.Entry) (int64, error) {
	if p.Review == nil {
		return p.baseRank(ctx, dir, entry)
	}
	merged, err := p.reviewChildren(ctx, dir)
	if err != nil {
		return 0, err
	}
	key := sourcecatalog.DirectoryOrder(entry.Name, entry.IsDir)
	node, err := p.reviewNode(ctx, entry.Path)
	if err != nil {
		return 0, err
	}
	if node.Visible && node.Virtual {
		key = sourcecatalog.OverlayOrder(node)
	}
	_, rank, err := merged.Locate(ctx, key)
	if err != nil {
		return 0, err
	}
	ancillary, _, err := p.ancillary(ctx, dir)
	if ancillary != "" {
		rank++
	}
	return rank, err
}
func (p *Projection) reviewChildren(ctx context.Context, dir string) (pagedview.WeightedUnion[sourcecatalog.TreeItem], error) {
	var union pagedview.WeightedUnion[sourcecatalog.TreeItem]
	node, err := p.reviewNode(ctx, dir)
	if err != nil {
		return union, err
	}
	index := &pagedview.RangeIndex[sourcecatalog.TreeItem]{Store: &pagedview.MemoryPages[sourcecatalog.TreeItem]{}}
	if !node.Visible || !node.Virtual {
		index, err = p.Navigation.Children(ctx, dir)
		if err != nil {
			return union, err
		}
	}
	rule, err := p.rule(ctx, dir)
	if err != nil {
		return union, err
	}
	var base pagedview.OrderedWeights[sourcecatalog.TreeItem] = pagedview.UnitWeights[sourcecatalog.TreeItem]{Index: index}
	if rule.Recursive {
		base = index

	}
	_, replacements, err := p.baseChildren(ctx, dir)
	if err != nil {
		return union, err
	}
	union.Base = &replacementWeights{base: base, replacements: replacements}
	union.Added, err = p.Review.index(ctx, dir)
	return union, err
}

type replacementWeights struct {
	base         pagedview.OrderedWeights[sourcecatalog.TreeItem]
	replacements []replacement
}

func (r *replacementWeights) Count(ctx context.Context) (int64, error) { return r.base.Count(ctx) }
func (r *replacementWeights) Extent(ctx context.Context) (int64, error) {
	total, err := r.base.Extent(ctx)
	if err != nil {
		return 0, err
	}
	for _, change := range r.replacements {
		total += change.weight - change.original
	}
	return total, nil
}
func (r *replacementWeights) SelectItem(ctx context.Context, rank int64) (pagedview.RangeItem[sourcecatalog.TreeItem], error) {
	item, err := r.base.SelectItem(ctx, rank)
	if err != nil {
		return item, err
	}
	for _, change := range r.replacements {
		if item.Value.Path == change.entry.Path {
			item.Weight = change.weight
			break
		}
	}
	return item, nil
}
func (r *replacementWeights) Locate(ctx context.Context, key string) (pagedview.RangeItem[sourcecatalog.TreeItem], int64, error) {
	item, rank, err := r.base.Locate(ctx, key)
	if err != nil && !errors.Is(err, pagedview.ErrMissing) {
		return item, rank, err
	}
	for _, change := range r.replacements {
		changedKey := sourcecatalog.DirectoryOrder(change.entry.Name, change.entry.IsDir)
		if changedKey < key {
			rank += change.weight - change.original
		} else if changedKey == key {
			item.Weight = change.weight
		}
	}
	return item, rank, err
}

func (r *ReviewProjection) clone() *ReviewProjection { return NewReviewProjection(r.overlay) }
