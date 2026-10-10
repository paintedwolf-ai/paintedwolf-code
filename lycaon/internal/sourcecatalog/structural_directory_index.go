package sourcecatalog

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/repochange"
)

type structuralDirectory struct {
	page        uint64
	observation DirectoryObservation
}

// Directory metadata shares the immutable segment lifetime of the file ranges.
type structuralDirectoryIndex struct {
	root  uint64
	count int
	store pagedview.PageStore[structuralDirectory]
}

func (index structuralDirectoryIndex) Len() int { return index.count }
func (index structuralDirectoryIndex) Get(ctx context.Context, key string) (structuralDirectory, bool, error) {
	if err := ctx.Err(); err != nil {
		return structuralDirectory{}, false, err
	}
	if index.root == 0 {
		return structuralDirectory{}, false, nil
	}
	tree := pagedview.RangeIndex[structuralDirectory]{Store: index.store, Root: index.root}
	item, _, err := tree.LocateItem(ctx, key)
	if errors.Is(err, pagedview.ErrMissing) {
		return structuralDirectory{}, false, nil
	}
	return item.Value, err == nil, err
}
func (index structuralDirectoryIndex) Visit(ctx context.Context, visit func(structuralDirectory) error) error {
	return index.visitAfter(ctx, "", "", visit)
}

// VisitChildren skips descendant key ranges without retaining directory names.
func (index structuralDirectoryIndex) VisitChildren(ctx context.Context, parent string, visit func(structuralDirectory) error) error {
	prefix := ""
	if parent != "." {
		prefix = parent + "/"
	}
	after, inclusive := prefix, false
	tree := pagedview.RangeIndex[structuralDirectory]{Store: index.store, Root: index.root}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// A slash-prefix upper bound can itself name an immediate child.
		if inclusive {
			value, found, err := index.Get(ctx, after)
			if err != nil {
				return err
			}
			if found {
				if err := visit(value); err != nil {
					return err
				}
			}
			inclusive = false
		}
		items, err := tree.ReadAfter(ctx, after, indexBatchSize)
		if err != nil || len(items) == 0 {
			return err
		}
		for _, item := range items {
			if !strings.HasPrefix(item.Key, prefix) {
				return nil
			}
			after = item.Key
			if item.Key == "." {
				continue
			}
			if slash := strings.IndexByte(item.Key[len(prefix):], '/'); slash >= 0 {
				after = item.Key[:len(prefix)+slash] + "0"
				inclusive = true
				break
			}
			if err := visit(item.Value); err != nil {
				return err
			}
		}
	}
}

func (index structuralDirectoryIndex) visitAfter(ctx context.Context, after, prefix string, visit func(structuralDirectory) error) error {
	tree := pagedview.RangeIndex[structuralDirectory]{Store: index.store, Root: index.root}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		items, err := tree.ReadAfter(ctx, after, indexBatchSize)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		for _, item := range items {
			if prefix != "" && !strings.HasPrefix(item.Key, prefix) {
				return nil
			}
			if err := visit(item.Value); err != nil {
				return err
			}
		}
		after = items[len(items)-1].Key
	}
}

const structuralDirectoryRecordLimit = 64 << 10

type structuralDirectoryWire struct {
	Page        uint64
	Path        []byte
	Failure     []byte
	Observation DirectoryObservation
}

func directoryWire(record structuralDirectory) structuralDirectoryWire {
	observation := record.observation
	observation.Path, observation.Failure = "", ""
	return structuralDirectoryWire{Page: record.page, Path: []byte(record.observation.Path), Failure: []byte(record.observation.Failure), Observation: observation}
}
func (wire structuralDirectoryWire) record() structuralDirectory {
	wire.Observation.Path = string(wire.Path)
	wire.Observation.Failure = string(wire.Failure)
	return structuralDirectory{page: wire.Page, observation: wire.Observation}
}
func encodeDirectoryRecord(record structuralDirectory) ([]byte, error) {
	body, err := json.Marshal(directoryWire(record))
	if err == nil && len(body) > structuralDirectoryRecordLimit {
		return nil, pagedview.ErrFrameSize
	}
	return body, err
}
func validateDirectoryRecord(record structuralDirectory) error {
	textBytes := len(record.observation.Path) + len(record.observation.Failure) + len(record.observation.Epoch.BootID)
	if textBytes <= (structuralDirectoryRecordLimit-1024)/6 {
		return nil
	}
	_, err := encodeDirectoryRecord(record)
	return err
}

type structuralDirectoryBranch struct {
	Key   []byte
	Page  uint64
	Count int64
}
type structuralDirectoryPage struct {
	Items    []structuralDirectoryWire
	Children []structuralDirectoryBranch
}
type structuralRecordReader interface {
	readRecord(context.Context, uint64) ([]byte, error)
}
type structuralRecordWriter interface {
	writeRecord(context.Context, []byte) (uint64, error)
}
type structuralDirectoryPages struct {
	reader structuralRecordReader
	writer structuralRecordWriter
	mu     sync.Mutex
	cache  *pagedview.Cache[uint64, pagedview.RangePage[structuralDirectory]]
}

func newStructuralDirectoryPages(reader structuralRecordReader, writer structuralRecordWriter) *structuralDirectoryPages {
	return &structuralDirectoryPages{reader: reader, writer: writer, cache: pagedview.NewCache[uint64, pagedview.RangePage[structuralDirectory]](512, 8<<20)}
}

// Read shares immutable cached slices; RangeIndex copies pages before modification.
func (pages *structuralDirectoryPages) Read(ctx context.Context, id uint64) (pagedview.RangePage[structuralDirectory], error) {
	pages.mu.Lock()
	value, found := pages.cache.Get(id)
	pages.mu.Unlock()
	if found {
		return value, nil
	}
	var result pagedview.RangePage[structuralDirectory]
	body, err := pages.reader.readRecord(ctx, id)
	if err != nil {
		return result, err
	}
	var encoded structuralDirectoryPage
	if err := json.Unmarshal(body, &encoded); err != nil {
		return result, err
	}
	if len(encoded.Items) > pagedview.PageFanout || len(encoded.Children) > pagedview.PageFanout || len(encoded.Items) > 0 && len(encoded.Children) > 0 {
		return result, pagedview.ErrRange
	}
	for _, item := range encoded.Items {
		record := item.record()
		result.Items = append(result.Items, pagedview.RangeItem[structuralDirectory]{Key: record.observation.Path, Value: record, Weight: 1})
	}
	for _, child := range encoded.Children {
		result.Children = append(result.Children, pagedview.Branch{Key: string(child.Key), Page: child.Page, Count: child.Count, Weight: child.Count})
	}
	pages.mu.Lock()
	pages.cache.Put(id, result, int64(len(body))*2)
	pages.mu.Unlock()
	return result, nil
}
func (pages *structuralDirectoryPages) Write(ctx context.Context, _ uint64, page pagedview.RangePage[structuralDirectory]) (uint64, error) {
	if pages.writer == nil {
		return 0, errors.New("published directory metadata is immutable")
	}
	var encoded structuralDirectoryPage
	for _, item := range page.Items {
		encoded.Items = append(encoded.Items, directoryWire(item.Value))
	}
	for _, child := range page.Children {
		encoded.Children = append(encoded.Children, structuralDirectoryBranch{Key: []byte(child.Key), Page: child.Page, Count: child.Count})
	}
	body, err := json.Marshal(encoded)
	if err != nil {
		return 0, err
	}
	id, err := pages.writer.writeRecord(ctx, body)
	if err == nil {
		// Cache ownership excludes the caller's mutable write buffers.
		page.Items = slices.Clone(page.Items)
		page.Children = slices.Clone(page.Children)
		pages.mu.Lock()
		pages.cache.Put(id, page, int64(len(body))*2)
		pages.mu.Unlock()
	}
	return id, err
}
func (*structuralDirectoryPages) Delete(context.Context, uint64) error { return nil }

type cachedDirectoryWork struct {
	entry structuralDirectoryWork
	found bool
}

func (w *structuralDirectoryWorkspace) statement(ctx context.Context, query string) (*sql.Stmt, error) {
	if statement := w.statements[query]; statement != nil {
		return statement, nil
	}
	statement, err := w.tx.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	w.statements[query] = statement
	return statement, nil
}
func (w *structuralDirectoryWorkspace) cacheWork(key string, entry structuralDirectoryWork, found bool) {
	if w.hot == nil {
		w.hot = pagedview.NewCache[string, cachedDirectoryWork](8192, 8<<20)
	}
	w.hot.Put(key, cachedDirectoryWork{entry: entry, found: found}, directoryWorkBytes(key, entry))
}
func (w *structuralDirectoryWorkspace) cachedWork(key string) (structuralDirectoryWork, bool, bool) {
	if w.hot == nil {
		return structuralDirectoryWork{}, false, false
	}
	cached, known := w.hot.Get(key)
	return cached.entry, cached.found, known
}

// Finalization consumes its work in one SQL pass, without decoding directory records.
func (w *structuralDirectoryWorkspace) ClearFlags(ctx context.Context, mask uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.tx == nil {
		for key, entry := range w.memory {
			entry.flags &^= mask
			w.memory[key] = entry
		}
		return nil
	}
	statement, err := w.statement(ctx, "UPDATE directories SET flags=flags & ~? WHERE (flags & ?) != 0") //nolint:sqlclosecheck // Workspace.Close releases this reusable statement.
	if err != nil {
		return err
	}
	_, err = statement.ExecContext(ctx, mask, mask)
	if err == nil {
		w.hot = nil
	}
	return err
}

// The fixed prefix holds ten 64-bit scalars followed by the completion byte.
const directoryWorkScalars = 10

// Scratch records omit the path already stored in SQLite's primary key.
func encodeDirectoryWorkRecord(record structuralDirectory) ([]byte, error) {
	observation := record.observation
	body := make([]byte, 0, directoryWorkScalars*8+16+len(observation.Failure)+len(observation.Epoch.BootID))
	for _, value := range []uint64{record.page, directoryWorkIntBits(observation.Sequence), directoryWorkIntBits(observation.FirstListed), directoryWorkIntBits(int64(observation.Entries)), observation.Invalidation, observation.Epoch.Value, directoryWorkIntBits(observation.Observed.Unix()), directoryWorkIntBits(int64(observation.Observed.Nanosecond())), directoryWorkIntBits(observation.Stamp.Modified), directoryWorkIntBits(observation.Stamp.Changed)} {
		body = binary.LittleEndian.AppendUint64(body, value)
	}
	complete := byte(0)
	if observation.Complete {
		complete = 1
	}
	body = append(body, complete)
	body = binary.AppendUvarint(body, uint64(len(observation.Failure)))
	body = append(body, observation.Failure...)
	body = append(body, observation.Epoch.BootID...)
	if len(body) > structuralDirectoryRecordLimit {
		return nil, pagedview.ErrFrameSize
	}
	return body, nil
}
func decodeDirectoryWorkRecord(key string, body []byte) (structuralDirectory, error) {
	const completeAt = directoryWorkScalars * 8
	if len(body) < completeAt+2 || len(body) > structuralDirectoryRecordLimit {
		return structuralDirectory{}, pagedview.ErrFrameSize
	}
	number := func(at int) uint64 { return binary.LittleEndian.Uint64(body[at*8 : at*8+8]) }
	failureLength, n := binary.Uvarint(body[completeAt+1:])
	if n <= 0 || body[completeAt] > 1 {
		return structuralDirectory{}, errors.New("invalid scratch directory record")
	}
	remaining := len(body) - completeAt - 1 - n
	if remaining < 0 {
		return structuralDirectory{}, errors.New("invalid scratch directory length")
	}
	if failureLength > uint64(remaining) {
		return structuralDirectory{}, errors.New("invalid scratch directory length")
	}
	entries := directoryWorkSignedBits(number(3))
	nanoseconds := directoryWorkSignedBits(number(7))
	if entries < 0 || entries > math.MaxInt || nanoseconds < 0 || nanoseconds >= 1e9 || failureLength > math.MaxInt {
		return structuralDirectory{}, errors.New("invalid scratch directory scalar")
	}
	start := completeAt + 1 + n
	return structuralDirectory{page: number(0), observation: DirectoryObservation{
		Path: key, Sequence: directoryWorkSignedBits(number(1)), FirstListed: directoryWorkSignedBits(number(2)), Entries: int(entries), Invalidation: number(4),
		Complete: body[completeAt] != 0, Observed: time.Unix(directoryWorkSignedBits(number(6)), nanoseconds).UTC(),
		Stamp:   DirectoryStamp{Modified: directoryWorkSignedBits(number(8)), Changed: directoryWorkSignedBits(number(9))},
		Failure: string(body[start : start+int(failureLength)]),
		Epoch:   repochange.Epoch{Value: number(5), BootID: string(body[start+int(failureLength):])},
	}}, nil
}

func directoryWorkIntBits(value int64) uint64 {
	return uint64(value) //nolint:gosec // G115: Scratch serialization preserves the signed two's-complement bits, including pre-epoch dates.
}
func directoryWorkSignedBits(value uint64) int64 {
	return int64(value) //nolint:gosec // G115: This reverses directoryWorkIntBits without changing its signed representation.
}

// Directory keys precede file keys, so entire file-only branches need no reads.
func visitStructuralDirectoryEdges(ctx context.Context, store pagedview.PageStore[TreeItem], root uint64, visit func(pagedview.RangeItem[TreeItem]) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if root == 0 {
		return nil
	}
	page, err := store.Read(ctx, root)
	if err != nil {
		return err
	}
	for _, item := range page.Items {
		if !directoryOrderKind(item.Key) {
			break
		}
		if err := visit(item); err != nil {
			return err
		}
	}
	for _, child := range page.Children {
		if !directoryOrderKind(child.Key) {
			break
		}
		if err := visitStructuralDirectoryEdges(ctx, store, child.Page, visit); err != nil {
			return err
		}
	}
	return nil
}
