package sourcecatalog

import (
	"context"
	"errors"
	"path"

	"github.com/lycaon/lycaon/internal/pagedview"
)

type navigationDirectory struct {
	index *pagedview.RangeIndex[TreeItem]
	state DirectoryState
}

// Navigation retains directory state and row coordinates across filesystem changes.
type Navigation struct {
	pin  *GenerationPin
	root Root
	// Generation is the structure generation this snapshot reads.
	Generation  int64
	pages       *structuralGeneration
	directories *pagedview.Cache[string, navigationDirectory]
	entries     *pagedview.Cache[string, Entry]
	reader      *structuralPageReader
}

// OpenNavigation reads the head generation.
func (c *Catalog) OpenNavigation(ctx context.Context, projectID string, root Root) (*Navigation, error) {
	store, err := c.indexStore(ctx, projectID, root)
	if err != nil {
		return nil, err
	}
	return openNavigation(ctx, store, headGeneration)
}

func openNavigation(ctx context.Context, store *indexStore, ceiling int64) (*Navigation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pin, err := store.retainGeneration(ceiling, ceiling == headGeneration)
	if err != nil {
		return nil, err
	}
	return &Navigation{pin: pin, root: store.root, Generation: pin.Generation, pages: pin.value,
		directories: pagedview.NewCache[string, navigationDirectory](512, 256<<10),
		entries:     pagedview.NewCache[string, Entry](1024, 512<<10)}, nil
}

// OpenNavigation opens another reader over this pin's immutable generation.
// It remains valid after the store retires; releasing this pin ends admission.
func (p *GenerationPin) OpenNavigation(ctx context.Context) (*Navigation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	clone, err := p.clone()
	if err != nil {
		return nil, err
	}
	return &Navigation{pin: clone, root: clone.store.root, Generation: clone.Generation, pages: clone.value,
		directories: pagedview.NewCache[string, navigationDirectory](512, 256<<10),
		entries:     pagedview.NewCache[string, Entry](1024, 512<<10)}, nil
}

// Retain keeps these coordinates readable after the transaction closes.
func (n *Navigation) Retain() (*GenerationPin, error) {
	return n.pin.clone()
}

func (n *Navigation) Close() error { n.pin.Release(); return nil }

func (n *Navigation) directory(ctx context.Context, dir string) (navigationDirectory, error) {
	if cached, ok := n.directories.Get(dir); ok {
		return cached, nil
	}
	if err := ctx.Err(); err != nil {
		return navigationDirectory{}, err
	}
	record, _, err := n.pages.directories.Get(ctx, dir)
	if err != nil {
		return navigationDirectory{}, err
	}
	index := &pagedview.RangeIndex[TreeItem]{Store: n.pageReader(), Root: record.page}
	state := stateOf(record.observation)
	found := navigationDirectory{index: index, state: state}
	n.directories.Put(dir, found, int64(160+len(dir)+len(state.Failure)))
	return found, nil
}

func (n *Navigation) Children(ctx context.Context, dir string) (*pagedview.RangeIndex[TreeItem], error) {
	directory, err := n.directory(ctx, dir)
	return directory.index, err
}

// State is what this generation knows about the directory's listing.
func (n *Navigation) State(ctx context.Context, dir string) (DirectoryState, error) {
	directory, err := n.directory(ctx, dir)
	return directory.state, err
}

// ListedEntries is how many entries the live listing has published so far. It sizes
// further demand; it is not part of the generation's coordinates.
func (n *Navigation) ListedEntries(ctx context.Context, dir string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	record, _, err := n.pages.directories.Get(ctx, dir)
	if err != nil {
		return 0, err
	}
	return record.observation.Entries, nil
}

// Entry resolves a path through its parent's index. Only structure is known here:
// path, name, kind, and link status.
func (n *Navigation) Entry(ctx context.Context, rel string) (Entry, error) {
	rel = normalizeDir(rel)
	if rel == "." {
		return Entry{RootID: n.root.ID, Path: ".", Parent: "", Name: ".", IsDir: true}, nil
	}
	if cached, ok := n.entries.Get(rel); ok {
		return cached, nil
	}
	parent := normalizeDir(path.Dir(rel))
	children, err := n.Children(ctx, parent)
	if err != nil {
		return Entry{}, err
	}
	name := path.Base(rel)
	for _, isDir := range []bool{true, false} {
		item, _, err := children.LocateItem(ctx, DirectoryOrder(name, isDir))
		if errors.Is(err, pagedview.ErrMissing) {
			continue
		}
		if err != nil {
			return Entry{}, err
		}
		entry := n.entryOf(item)
		n.entries.Put(rel, entry, int64(192+2*len(rel)+len(parent)))
		return entry, nil
	}
	return Entry{}, pagedview.ErrMissing
}

func (n *Navigation) entryOf(item pagedview.RangeItem[TreeItem]) Entry {
	rel := item.Value.Path
	return Entry{RootID: n.root.ID, Path: rel, Parent: normalizeDir(path.Dir(rel)), Name: path.Base(rel), Depth: pathDepth(rel),
		IsDir: directoryOrderKind(item.Key), IsSymlink: item.Value.Symlink}
}

func (n *Navigation) Child(ctx context.Context, dir string, rank int64, recursive bool) (Entry, int64, error) {
	children, err := n.Children(ctx, dir)
	if err != nil {
		return Entry{}, 0, err
	}
	var item pagedview.RangeItem[TreeItem]
	var offset int64
	if recursive {
		item, offset, err = children.Select(ctx, rank)
	} else {
		item, err = children.SelectItem(ctx, rank)
	}
	if err != nil {
		return Entry{}, 0, err
	}
	return n.entryOf(item), offset, nil
}

func (n *Navigation) ChildRank(ctx context.Context, dir string, entry Entry, recursive bool) (int64, int64, error) {
	children, err := n.Children(ctx, dir)
	if err != nil {
		return 0, 0, err
	}
	var item pagedview.RangeItem[TreeItem]
	var rank int64
	key := DirectoryOrder(entry.Name, entry.IsDir)
	if recursive {
		item, rank, err = children.Locate(ctx, key)
	} else {
		item, rank, err = children.LocateItem(ctx, key)
		item.Weight = 1
	}
	return rank, item.Weight, err
}

// Each frame bounds decoded pages while generations retain compact bytes.
func (n *Navigation) pageReader() *structuralPageReader {
	if n.reader == nil {
		n.reader = &structuralPageReader{generation: n.pages, shared: &n.pin.store.catalog.structurePages, cacheID: n.pin.store.instance, cache: pagedview.NewCache[uint64, pagedview.RangePage[TreeItem]](128, 2<<20)}
	}
	return n.reader
}

type structuralPageReader struct {
	shared     *structurePageCache
	cacheID    uint64
	generation *structuralGeneration
	cache      *pagedview.Cache[uint64, pagedview.RangePage[TreeItem]]
}

func (r *structuralPageReader) Read(ctx context.Context, id uint64) (pagedview.RangePage[TreeItem], error) {
	if page, ok := r.cache.Get(id); ok {
		return page, nil
	}
	if r.shared != nil {
		if page, found := r.shared.get(structurePageKey{r.cacheID, id}); found {
			r.cache.Put(id, page, rangePageBytes(page))
			return page, nil
		}
	}
	page, err := r.generation.Read(ctx, id)
	if err == nil {
		r.cache.Put(id, page, rangePageBytes(page))
		if r.shared != nil {
			r.shared.put(structurePageKey{r.cacheID, id}, page)
		}
	}
	return page, err
}
func (r *structuralPageReader) Write(ctx context.Context, id uint64, page pagedview.RangePage[TreeItem]) (uint64, error) {
	return r.generation.Write(ctx, id, page)
}
func (r *structuralPageReader) Delete(ctx context.Context, id uint64) error {
	return r.generation.Delete(ctx, id)
}
