package sourcetree

import (
	"context"
	"errors"
	"path"
	"sort"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

type Row struct {
	Address    Address
	Name, Kind string
	Error      string
	Depth      int
	Expanded   bool
	Symlink    bool
	Deleted    bool
}
type Projection struct {
	Root       string
	Navigation *sourcecatalog.Navigation
	Rules      *Rules
	Review     *ReviewProjection
	baseCache  *pagedview.Cache[string, projectedChildren]
}
type replacement struct {
	entry                  sourcecatalog.Entry
	rank, original, weight int64
}

func (p *Projection) open(ctx context.Context, dir string) (bool, error) {
	node, err := p.reviewNode(ctx, dir)
	if err != nil {
		return false, err
	}
	if node.Visible && node.Virtual {
		return p.Rules.atDefault(Address{Root: p.Root, Path: dir}, true).Open, nil
	}
	rule, err := p.rule(ctx, dir)
	if err != nil {
		return false, err
	}
	if !rule.Open || !rule.Recursive || rule.Explicit || dir == "." {
		return rule.Open, nil
	}
	state, err := p.Navigation.State(ctx, dir)
	return state.Listed, err
}

type projectedChildren struct {
	total        int64
	replacements []replacement
}

func (p *Projection) baseChildren(ctx context.Context, dir string) (int64, []replacement, error) {
	if p.baseCache != nil {
		if saved, ok := p.baseCache.Get(dir); ok {
			return saved.total, saved.replacements, nil
		}
	}
	total, replacements, err := p.computeBaseChildren(ctx, dir)
	if err == nil && p.baseCache != nil {
		size := int64(128 + len(dir))
		for _, item := range replacements {
			size += int64(192 + len(item.entry.Path) + len(item.entry.Name) + len(item.entry.Parent))
		}
		p.baseCache.Put(dir, projectedChildren{total, replacements}, size)
	}
	return total, replacements, err
}
func (p *Projection) computeBaseChildren(ctx context.Context, dir string) (int64, []replacement, error) {
	node, err := p.reviewNode(ctx, dir)
	if err != nil {
		return 0, nil, err
	}
	if node.Visible && node.Virtual {
		return 0, nil, nil
	}
	open, err := p.open(ctx, dir)
	if err != nil || !open {
		return 0, nil, err
	}
	rule, err := p.rule(ctx, dir)
	if err != nil {
		return 0, nil, err
	}
	index, err := p.Navigation.Children(ctx, dir)
	if err != nil {
		return 0, nil, err
	}
	total, err := index.Count(ctx)
	if rule.Recursive {
		total, err = index.Extent(ctx)
	}
	if err != nil {
		return 0, nil, err
	}
	replacements := []replacement{}
	for _, child := range p.Rules.Branches(Address{Root: p.Root, Path: dir}) {
		entry, err := p.Navigation.Entry(ctx, child)
		if errors.Is(err, pagedview.ErrMissing) {
			continue
		}
		if err != nil {
			return 0, nil, err
		}
		if !entry.IsDir {
			continue
		}
		rank, original, err := p.Navigation.ChildRank(ctx, dir, entry, rule.Recursive)

		if err != nil {
			return 0, nil, err
		}
		nested, _, err := p.baseChildren(ctx, child)
		if err != nil {
			return 0, nil, err
		}
		childOpen, err := p.open(ctx, child)
		if err != nil {
			return 0, nil, err
		}
		weight := int64(1)
		if childOpen {
			weight += nested
		}
		total += weight - original
		replacements = append(replacements, replacement{entry: entry, rank: rank, original: original, weight: weight})
	}
	sort.Slice(replacements, func(i, j int) bool { return replacements[i].rank < replacements[j].rank })
	state, err := p.Navigation.State(ctx, dir)
	if err != nil {
		return 0, nil, err
	}
	if sourcecatalog.DirectoryAncillary(state, total) != "" {
		total++
	}
	return total, replacements, nil
}

func (p *Projection) Count(ctx context.Context) (int64, error) {
	children, _, err := p.children(ctx, ".")
	if !p.Rules.At(Address{Root: p.Root, Path: "."}).Open {
		return 1, err
	}
	return 1 + children, err
}

func (p *Projection) baseChild(ctx context.Context, dir string, rank int64) (sourcecatalog.Entry, int64, error) {
	_, replacements, err := p.baseChildren(ctx, dir)
	if err != nil {
		return sourcecatalog.Entry{}, 0, err
	}
	rule, err := p.rule(ctx, dir)
	if err != nil {
		return sourcecatalog.Entry{}, 0, err
	}
	delta := int64(0)
	for _, replacement := range replacements {
		start := replacement.rank + delta
		if rank < start {
			break
		}
		if rank < start+replacement.weight {
			return replacement.entry, rank - start, nil
		}
		delta += replacement.weight - replacement.original
	}

	return p.Navigation.Child(ctx, dir, rank-delta, rule.Recursive)
}

func (p *Projection) Select(ctx context.Context, rank int64) (Row, error) {
	if rank < 0 {
		return Row{}, pagedview.ErrRange
	}
	row := Row{Address: Address{Root: p.Root, Path: "."}, Kind: "directory", Expanded: p.Rules.At(Address{Root: p.Root, Path: "."}).Open}
	for {
		if rank == 0 {
			return row, nil
		}
		if !row.Expanded {
			return Row{}, pagedview.ErrRange
		}
		rank--
		ancillary, message, err := p.ancillary(ctx, row.Address.Path)
		if err != nil {
			return Row{}, err
		}
		if ancillary != "" {
			if rank == 0 {
				row.Depth++
				row.Expanded = false
				row.Kind = ancillary
				row.Error = message
				return row, nil
			}
			rank--
		}

		entry, offset, err := p.child(ctx, row.Address.Path, rank)
		if err != nil {
			return Row{}, err
		}

		row = Row{Address: Address{Root: p.Root, Path: entry.Path}, Name: entry.Name, Kind: "file", Depth: row.Depth + 1, Symlink: entry.IsSymlink}
		node, err := p.reviewNode(ctx, entry.Path)
		if err != nil {
			return Row{}, err
		}
		row.Deleted = node.Visible && node.Virtual
		if entry.IsDir {
			row.Kind = "directory"
			row.Expanded, err = p.open(ctx, entry.Path)
			if err != nil {
				return Row{}, err
			}
		}
		rank = offset
	}
}

func (p *Projection) Frame(ctx context.Context, offset int64, limit int) ([]Row, int64, error) {
	if offset < 0 || limit < 1 || limit > pagedview.MaxRows {
		return nil, 0, pagedview.ErrRange
	}
	reader := *p
	reader.baseCache = pagedview.NewCache[string, projectedChildren](128, 2<<20)
	total, err := reader.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	if offset > total {
		return nil, 0, pagedview.ErrRange
	}
	rows, err := reader.frameRows(ctx, offset, limit, total)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (p *Projection) recursiveAllowed(ctx context.Context, dir string) (bool, error) {
	for current := dir; current != "."; current = path.Dir(current) {
		entry, err := p.Navigation.Entry(ctx, current)
		if err != nil {
			return false, err
		}
		if entry.IsSymlink {
			return false, nil
		}
	}
	return true, nil
}
func (p *Projection) rule(ctx context.Context, dir string) (Disclosure, error) {
	node, err := p.reviewNode(ctx, dir)
	if err != nil {
		return Disclosure{}, err
	}
	if node.Visible && node.Virtual {
		return p.Rules.atDefault(Address{Root: p.Root, Path: dir}, true), nil
	}
	rule := p.Rules.At(Address{Root: p.Root, Path: dir})
	if rule.Recursive {
		allowed, err := p.recursiveAllowed(ctx, dir)
		if err != nil {
			return Disclosure{}, err
		}
		if !allowed {
			rule.Recursive = false
			if !rule.Explicit {
				rule.Open = false
			}
		}
	}
	return rule, nil
}
