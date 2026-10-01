package sourcecatalog

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"

	"github.com/lycaon/lycaon/internal/pagedview"
)

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
