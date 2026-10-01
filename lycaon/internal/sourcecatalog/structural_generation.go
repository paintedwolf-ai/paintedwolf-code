package sourcecatalog

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"

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
	b := &structuralBuilder{base: base, comparison: base, writer: writer,
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
