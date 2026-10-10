package sourcecatalog

import (
	"context"
	"errors"
	"path"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/pagedview"
)

const structuralMemoryLimit = 32 << 20
const structuralOffsetBits = 40
const structuralOffsetMask = (uint64(1) << structuralOffsetBits) - 1

var structuralSegmentSerial atomic.Uint32

var errStructuralGenerationAddress = errors.New("structural generation address space exhausted")

type structuralSegment struct {
	bytes *structuralSegments
	refs  atomic.Int64
}

func (s *structuralSegment) retain() { s.refs.Add(1) }
func (s *structuralSegment) release() {
	if s.refs.Add(-1) == 0 {
		_ = s.bytes.Close()
	}
}

// A generation shares sealed segments and a persistent directory index.
type structuralGeneration struct {
	id                  int64
	complete            bool
	directories         structuralDirectoryIndex
	segments            map[uint32]*structuralSegment
	encodedBytes        int64
	compactionDebtBytes int64
}

func (g *structuralGeneration) close() {
	for _, segment := range g.segments {
		segment.release()
	}
}

func (g *structuralGeneration) Read(ctx context.Context, id uint64) (pagedview.RangePage[TreeItem], error) {
	segment := g.segments[uint32(id>>structuralOffsetBits)]
	if segment == nil {
		return pagedview.RangePage[TreeItem]{}, pagedview.ErrMissing
	}
	body, err := segment.bytes.Read(ctx, id&structuralOffsetMask)
	if err != nil {
		return pagedview.RangePage[TreeItem]{}, err
	}
	return decodeRangePage(body)
}
func (*structuralGeneration) Write(context.Context, uint64, pagedview.RangePage[TreeItem]) (uint64, error) {
	return 0, errors.New("published structure is immutable")
}
func (*structuralGeneration) Delete(context.Context, uint64) error {
	return errors.New("published structure is immutable")
}

type structuralBuilder struct {
	policy         walkPolicy
	base           *structuralGeneration
	comparison     *structuralGeneration
	directories    *structuralDirectoryWorkspace
	writer         *structuralSegmentWriter
	cache          *pagedview.Cache[uint64, pagedview.RangePage[TreeItem]]
	directoryPages *structuralDirectoryPages
	finalized      bool
	reverseMerged  bool
	coverage       func(context.Context, *structuralBuilder) error
	extra          map[uint32]*structuralSegment
	rebased        []*GenerationPin
	ownedBases     []*structuralGeneration
}

func newStructuralBuilder(store *indexStore, base *structuralGeneration) (*structuralBuilder, error) {
	writer, err := newStructuralSegmentWriter(filepath.Dir(store.structureFile), structuralMemoryLimit)
	if err != nil {
		return nil, err
	}
	b := &structuralBuilder{policy: store.policy, base: base, comparison: base, writer: writer,
		cache: pagedview.NewCache[uint64, pagedview.RangePage[TreeItem]](256, 4<<20)}
	var previous structuralDirectoryIndex
	if base != nil {
		previous = base.directories
	}
	b.directories, err = newStructuralDirectoryWorkspace(filepath.Dir(store.structureFile), previous)
	if err != nil {
		_ = writer.Close()
		return nil, err
	}
	b.directoryPages = newStructuralDirectoryPages(b, b)
	return b, nil
}

func (b *structuralBuilder) close() {
	_ = b.directories.Close()
	_ = b.writer.Close()
	for _, segment := range b.extra {
		segment.release()
	}
	for _, pin := range b.rebased {
		pin.Release()
	}
	for _, base := range b.ownedBases {
		base.close()
	}
}
func (g *structuralGeneration) readRecord(ctx context.Context, id uint64) ([]byte, error) {
	segment := g.segments[uint32(id>>structuralOffsetBits)]
	if segment == nil {
		return nil, pagedview.ErrMissing
	}
	return segment.bytes.Read(ctx, id&structuralOffsetMask)
}

func (b *structuralBuilder) readRecord(ctx context.Context, id uint64) ([]byte, error) {
	body, err := b.writer.Read(ctx, id)
	if err == nil || !errors.Is(err, pagedview.ErrMissing) {
		return body, err
	}
	if segment := b.extra[uint32(id>>structuralOffsetBits)]; segment != nil {
		return segment.bytes.Read(ctx, id&structuralOffsetMask)
	}
	if b.base != nil {
		return b.base.readRecord(ctx, id)
	}
	return nil, pagedview.ErrMissing
}

func (b *structuralBuilder) writeRecord(ctx context.Context, body []byte) (uint64, error) {
	return b.writer.Append(ctx, body)
}

func (b *structuralBuilder) Read(ctx context.Context, id uint64) (pagedview.RangePage[TreeItem], error) {
	if page, ok := b.cache.Get(id); ok {
		return page, nil
	}
	body, err := b.readRecord(ctx, id)
	if err != nil {
		return pagedview.RangePage[TreeItem]{}, err
	}
	page, err := decodeRangePage(body)
	if err == nil {
		b.cache.Put(id, page, rangePageBytes(page))
	}
	return page, err
}
func (b *structuralBuilder) Write(ctx context.Context, _ uint64, page pagedview.RangePage[TreeItem]) (uint64, error) {
	body, err := encodeRangePage(page)
	if err != nil {
		return 0, err
	}
	id, err := b.writeRecord(ctx, body)
	if err != nil {
		return 0, err
	}
	b.cache.Put(id, page, rangePageBytes(page))
	return id, nil
}
func (*structuralBuilder) Delete(context.Context, uint64) error { return nil }
func (b *structuralBuilder) children(ctx context.Context, dir string) (*pagedview.RangeIndex[TreeItem], DirectoryObservation, error) {
	directory, _, err := b.directories.Get(ctx, dir)
	return &pagedview.RangeIndex[TreeItem]{Store: b, Root: directory.page}, directory.observation, err
}
func (b *structuralBuilder) save(ctx context.Context, index *pagedview.RangeIndex[TreeItem], observation DirectoryObservation) error {
	return b.directories.Set(ctx, observation.Path, structuralDirectory{page: index.Root, observation: observation})
}
func (b *structuralBuilder) seal(ctx context.Context, id int64) (*structuralGeneration, error) {
	if id < 0 || id >= headGeneration {
		return nil, errStructuralGenerationAddress
	}
	children, observation, err := b.children(ctx, ".")
	if err != nil {
		return nil, err
	}
	pending, err := directoryUnresolved(ctx, children, stateOf(observation))
	if err != nil {
		return nil, err
	}
	index, err := b.directories.freeze(ctx, b.directoryPages)
	if err != nil {
		return nil, err
	}
	frozen, err := b.writer.Freeze()
	if err != nil {
		return nil, err
	}
	g := &structuralGeneration{id: id, complete: pending == 0, directories: index, segments: make(map[uint32]*structuralSegment)}
	if b.base != nil {
		g.compactionDebtBytes = b.base.compactionDebtBytes + b.writer.TotalBytes()
		for key, segment := range b.base.segments {
			segment.retain()
			g.segments[key] = segment
		}
	}
	for key, segment := range b.extra {
		if _, found := g.segments[key]; !found {
			segment.retain()
			g.segments[key] = segment
		}
	}
	for key, segment := range frozen {
		if _, found := g.segments[key]; found {
			segment.release()
			continue
		}
		g.segments[key] = segment
	}
	for _, segment := range g.segments {
		g.encodedBytes += segment.bytes.Bytes()
	}
	g.directories.store = newStructuralDirectoryPages(g, nil)
	return g, nil
}

var structuralObservationSerial atomic.Int64

func (b *structuralBuilder) item(ctx context.Context, node indexNode) (pagedview.RangeItem[TreeItem], error) {
	kind := "file"
	if node.isDir {
		kind = "directory"
	}
	boundary := node.isDir && b.policy.boundaryDir(node.path) != ""
	baseline := TreeRowFingerprint(node.path, kind, node.isSymlink, boundary, "")
	item := pagedview.RangeItem[TreeItem]{Key: DirectoryOrder(node.name, node.isDir), Value: TreeItem{Path: node.path, Symlink: node.isSymlink},
		Weight: 1, Fingerprint: baseline, BaselineFingerprint: baseline}
	if !node.isDir || node.isSymlink || b.policy.boundaryDir(node.path) != "" {
		return item, nil
	}
	index, observation, err := b.children(ctx, node.path)
	if err != nil {
		return item, err
	}
	state := stateOf(observation)
	item.Unresolved = 1
	if !state.Listed {
		return item, nil
	}
	size, err := index.Extent(ctx)
	if err != nil {
		return item, err
	}
	body, err := DirectoryBodyFingerprint(ctx, index, node.path, state, true)
	if err != nil {
		return item, err
	}
	item.Fingerprint = TreeRowFingerprint(node.path, kind, false, true, "").Combine(body)
	item.Weight += directoryBodyRows(state, size)
	item.Unresolved, err = directoryUnresolved(ctx, index, state)
	return item, err
}

// Each directory starts with an empty child range, retaining valid descendant records.
func (b *structuralBuilder) observe(ctx context.Context, listing directoryDiscovery) error {
	b.finalized = false
	observation := listing.observation
	dir := observation.Path
	index, previous, err := b.children(ctx, dir)
	if err != nil {
		return err
	}
	flags, err := b.directories.Flags(ctx, dir)
	if err != nil {
		return err
	}
	if flags&directoryStarted == 0 {
		index.Root = 0
		observation.Sequence = structuralObservationSerial.Add(1)
		observation.FirstListed = observation.Sequence
		if err := b.directories.UpdateFlags(ctx, dir, directoryDirty|directoryRepair|directoryStarted|directoryBranchesKnown, directoryHasBranches); err != nil {
			return err
		}
	} else {
		observation.Sequence, observation.FirstListed = previous.Sequence, previous.FirstListed
	}
	if err := b.directories.UpdateFlags(ctx, dir, directoryRepair, 0); err != nil {
		return err
	}
	observation.Observed = time.Now()
	items := make([]pagedview.RangeItem[TreeItem], 0, len(listing.nodes))
	for _, node := range listing.nodes {
		if node.isDir && !node.isSymlink {
			if err := b.directories.UpdateFlags(ctx, dir, directoryHasBranches, 0); err != nil {
				return err
			}
		}
		item, err := b.item(ctx, node)
		if err != nil {
			return err
		}
		item.Value.Sequence = observation.Sequence
		items = append(items, item)
	}
	if err := index.SetBatch(ctx, items); err != nil {
		return err
	}
	if err := b.save(ctx, index, observation); err != nil {
		return err
	}
	if observation.Complete {
		return b.revalidate(ctx, dir)
	}
	return nil
}

// Pruning compares complete memberships, so a partial listing cannot remove a child.
func (b *structuralBuilder) prune(ctx context.Context, dir string) error {
	var source pagedview.PageStore[TreeItem] = b
	old, found, err := b.directories.Previous(ctx, dir)
	if err != nil {
		return err
	}
	if !found && b.base != nil {
		old, found, err = b.base.directories.Get(ctx, dir)
		if err != nil {
			return err
		}
		source = b.base
	}
	if !found {
		return nil
	}
	current, observation, err := b.children(ctx, dir)
	if err != nil {
		return err
	}
	if !observation.Complete {
		return nil
	}
	return visitStructuralDirectoryEdges(ctx, source, old.page, func(entry pagedview.RangeItem[TreeItem]) error {
		now, _, err := current.LocateItem(ctx, entry.Key)
		if err != nil && !errors.Is(err, pagedview.ErrMissing) {
			return err
		}
		if err != nil || now.Value.Symlink != entry.Value.Symlink {
			return b.directories.DeleteSubtree(ctx, entry.Value.Path)
		}
		return nil
	})
}

// Descendants finish first; each affected directory repairs its child summaries once.
func (b *structuralBuilder) finalize(ctx context.Context) error {
	if b.finalized {
		return nil
	}
	err := b.directories.VisitFlags(ctx, directoryRepair, false, func(dir string, _ uint64) error {
		return b.prepareDirectoryRepair(ctx, dir)
	})
	if err != nil {
		return err
	}
	if err := b.directories.VisitFlagKeys(ctx, directoryAncestor, true, func(dir string, flags uint64) error {
		if dir == "." {
			return nil
		}
		return b.weighFlags(ctx, dir, flags)
	}); err != nil {
		return err
	}
	flags, err := b.directories.Flags(ctx, ".")
	if err != nil {
		return err
	}
	if flags&directoryAncestor != 0 {
		if err := b.weighFlags(ctx, ".", flags); err != nil {
			return err
		}
	}
	if err := b.directories.ClearFlags(ctx, directoryRepair|directoryAncestor); err != nil {
		return err
	}
	b.finalized = true
	return nil
}

func (b *structuralBuilder) prepareDirectoryRepair(ctx context.Context, dir string) error {
	if _, found, err := b.directories.Get(ctx, dir); err != nil || !found {
		return err
	}
	if dir != "." {
		parentPath := path.Dir(dir)
		parent, observed, err := b.children(ctx, parentPath)
		if err != nil {
			return err
		}
		flags, err := b.directories.Flags(ctx, parentPath)
		if err != nil {
			return err
		}
		if flags&directoryDirty != 0 && observed.FirstListed > 0 && observed.Complete {
			item, _, err := parent.LocateItem(ctx, DirectoryOrder(path.Base(dir), true))
			if errors.Is(err, pagedview.ErrMissing) || err == nil && item.Value.Symlink {
				return b.directories.DeleteSubtree(ctx, dir)
			}
			if err != nil {
				return err
			}
		}
	}
	if err := b.prune(ctx, dir); err != nil {
		return err
	}
	for current := dir; ; current = path.Dir(current) {
		flags, err := b.directories.Flags(ctx, current)
		if err != nil {
			return err
		}
		if flags&directoryAncestor != 0 {
			break
		}
		if err := b.directories.UpdateFlags(ctx, current, directoryAncestor, 0); err != nil {
			return err
		}
		if current == "." {
			break
		}
	}
	return nil
}

func (b *structuralBuilder) weighFlags(ctx context.Context, dir string, flags uint64) error {
	if flags&directoryBranchesKnown != 0 && flags&directoryHasBranches == 0 {
		return nil
	}
	index, observation, err := b.children(ctx, dir)
	if err != nil {
		return err
	}
	if observation.Path == "" {
		return nil
	}
	updates := make([]pagedview.RangeItem[TreeItem], 0, indexBatchSize)
	flush := func() error {
		if err := index.SetBatch(ctx, updates); err != nil {
			return err
		}
		updates = updates[:0]
		return nil
	}
	err = visitStructuralDirectoryEdges(ctx, b, index.Root, func(entry pagedview.RangeItem[TreeItem]) error {
		if entry.Value.Symlink {
			return nil
		}
		item, err := b.item(ctx, indexNode{path: entry.Value.Path, name: path.Base(entry.Value.Path), isDir: true})
		if err != nil {
			return err
		}
		item.Value.Sequence = entry.Value.Sequence
		if item != entry {
			updates = append(updates, item)
		}
		if len(updates) == indexBatchSize {
			return flush()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	return b.save(ctx, index, observation)
}
