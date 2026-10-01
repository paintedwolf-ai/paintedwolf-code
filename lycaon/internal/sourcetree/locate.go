package sourcetree

import (
	"context"
	"errors"
	"path"
	"strings"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

type Location struct {
	Address Address
	Index   int64
	Visible bool
	Pending bool
}
type Ancestor struct {
	Address    Address
	Name       string
	Depth      int
	Index, End int64
	Deleted    bool
}

// Locate is read-only. A hidden descendant resolves to its nearest visible ancestor.
func (p *Projection) Locate(ctx context.Context, rel string) (Location, error) {
	location := Location{Address: Address{Root: p.Root, Path: "."}, Visible: rel == "."}
	if rel == "." {
		return location, nil
	}
	for _, segment := range strings.Split(rel, "/") {
		dir := location.Address.Path
		open, err := p.open(ctx, dir)
		if err != nil {
			return Location{}, err
		}
		if !open {
			return location, nil
		}
		child := path.Join(dir, segment)
		entry, err := p.entry(ctx, child)
		if errors.Is(err, pagedview.ErrMissing) {
			state, readErr := p.Navigation.State(ctx, dir)
			location.Pending = !state.Complete && state.Failure == ""
			return location, readErr
		}
		if err != nil {
			return Location{}, err
		}
		rank, err := p.rank(ctx, dir, entry)
		if errors.Is(err, pagedview.ErrMissing) {
			return location, nil
		}
		if err != nil {
			return Location{}, err
		}
		location.Index += 1 + rank
		location.Address.Path = child
	}
	location.Visible = true
	return location, nil
}

func (p *Projection) baseRank(ctx context.Context, dir string, entry sourcecatalog.Entry) (int64, error) {
	_, replacements, err := p.baseChildren(ctx, dir)
	if err != nil {
		return 0, err
	}
	rule, err := p.rule(ctx, dir)
	if err != nil {
		return 0, err
	}
	rank, _, err := p.Navigation.ChildRank(ctx, dir, entry, rule.Recursive)

	if err != nil {
		return 0, err
	}
	delta := int64(0)
	for _, replacement := range replacements {
		if replacement.rank >= rank {
			break
		}
		delta += replacement.weight - replacement.original
	}
	state, err := p.Navigation.State(ctx, dir)
	if err != nil {
		return 0, err
	}
	index, err := p.Navigation.Children(ctx, dir)
	if err != nil {
		return 0, err
	}
	count, err := index.Count(ctx)
	if err != nil {
		return 0, err
	}
	if sourcecatalog.DirectoryAncillary(state, count) != "" {
		delta++
	}
	return rank + delta, nil
}

func (p *Projection) Ancestors(ctx context.Context, row Row) ([]Ancestor, error) {
	dir := path.Dir(row.Address.Path)
	if row.Kind != "file" && row.Kind != "directory" {
		dir = row.Address.Path
	}
	if row.Address.Path == "." && row.Kind == "directory" {
		return nil, nil
	}
	paths := []string{}
	for {
		paths = append(paths, dir)
		if dir == "." {
			break
		}
		dir = path.Dir(dir)
	}
	ancestors := make([]Ancestor, 0, len(paths))
	for i := len(paths) - 1; i >= 0; i-- {
		var parent *Ancestor
		if len(ancestors) > 0 {
			parent = &ancestors[len(ancestors)-1]
		}
		index, err := p.ancestorIndex(ctx, paths[i], parent)
		if err != nil {
			return nil, err
		}
		children, _, err := p.children(ctx, paths[i])
		if err != nil {
			return nil, err
		}
		node, err := p.reviewNode(ctx, paths[i])
		if err != nil {
			return nil, err
		}
		ancestors = append(ancestors, Ancestor{Deleted: node.Visible && node.Virtual, Address: Address{Root: p.Root, Path: paths[i]}, Name: path.Base(paths[i]), Depth: len(paths) - i - 1, Index: index, End: index + 1 + children})
	}
	return ancestors, nil
}

// Each ancestor rank builds on its parent's rank.
func (p *Projection) ancestorIndex(ctx context.Context, rel string, parent *Ancestor) (int64, error) {
	if parent == nil {
		return 0, nil
	}
	open, err := p.open(ctx, parent.Address.Path)
	if err != nil {
		return 0, err
	}
	if !open {
		return 0, errors.New("tree ancestor is outside its projection")
	}
	entry, err := p.entry(ctx, rel)
	if err != nil {
		return 0, err
	}
	rank, err := p.rank(ctx, parent.Address.Path, entry)
	return parent.Index + 1 + rank, err
}

func validAddress(address Address) bool {
	if address.Root == "" || address.Path == "" || strings.HasPrefix(address.Path, "/") || strings.ContainsRune(address.Path, 0) {
		return false
	}
	return path.Clean(address.Path) == address.Path && address.Path != ".." && !strings.HasPrefix(address.Path, "../")
}

var ErrAddress = errors.New("invalid tree address")

// ErrUnknownRoot reports an address whose folder root is not part of the view.
var ErrUnknownRoot = errors.New("folder root is not part of this tree")
