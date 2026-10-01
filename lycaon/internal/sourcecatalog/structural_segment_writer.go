package sourcecatalog

import (
	"context"
	"os"
	"sync"

	"github.com/lycaon/lycaon/internal/pagedview"
)

const structuralSegmentWriterTarget = 256 << 20

type structuralSegmentWriter struct {
	mu          sync.RWMutex
	dir         string
	memoryLimit int64
	targetBytes int64
	segments    map[uint32]*structuralSegment
	currentID   uint32
	current     *structuralSegment
	total       int64
	closed      bool
}

func newStructuralSegmentWriter(dir string, memoryLimit int64) (*structuralSegmentWriter, error) {
	return newStructuralSegmentWriterTarget(dir, memoryLimit, structuralSegmentWriterTarget)
}

func newStructuralSegmentWriterTarget(dir string, memoryLimit, targetBytes int64) (*structuralSegmentWriter, error) {
	if dir == "" || memoryLimit < 0 || targetBytes <= 0 {
		return nil, os.ErrInvalid
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &structuralSegmentWriter{dir: dir, memoryLimit: memoryLimit, targetBytes: targetBytes, segments: make(map[uint32]*structuralSegment)}, nil
}

func (w *structuralSegmentWriter) Append(ctx context.Context, body []byte) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	recordBytes := structuralRecordHeaderSize + int64(len(body))
	if len(body) > maxStructuralSegmentEntry || recordBytes > structuralSegmentsMaxBytes {
		return 0, os.ErrInvalid
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, errStructuralSegmentsClosed
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if w.current == nil || w.shouldRollLocked(recordBytes) {
		if err := w.openSegmentLocked(); err != nil {
			return 0, err
		}
	}
	local, err := w.current.bytes.Append(ctx, body)
	if err != nil {
		return 0, err
	}
	w.total += recordBytes
	return uint64(w.currentID)<<structuralOffsetBits | local, nil
}

func (w *structuralSegmentWriter) shouldRollLocked(recordBytes int64) bool {
	if w.current == nil {
		return true
	}
	current := w.current.bytes.Bytes()
	if current == 0 {
		return false
	}
	if current > structuralSegmentsMaxBytes-recordBytes {
		return true
	}
	return current+recordBytes > w.targetBytes
}

func (w *structuralSegmentWriter) openSegmentLocked() error {
	if w.current != nil {
		if err := w.current.bytes.Seal(); err != nil {
			return err
		}
	}
	id := structuralSegmentSerial.Add(1)
	if id == 0 || id >= 1<<(64-structuralOffsetBits) {
		return errStructuralSegmentAddress
	}
	bytes, err := newStructuralSegments(w.dir, w.memoryLimit)
	if err != nil {
		return err
	}
	segment := &structuralSegment{bytes: bytes}
	segment.retain()
	w.currentID, w.current = id, segment
	w.segments[id] = segment
	return nil
}

func (w *structuralSegmentWriter) Read(ctx context.Context, id uint64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	segmentID := uint32(id >> structuralOffsetBits)
	local := id & structuralOffsetMask
	if segmentID == 0 || local == 0 {
		return nil, errStructuralSegmentID
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return nil, errStructuralSegmentsClosed
	}
	segment := w.segments[segmentID]
	if segment == nil {
		return nil, pagedview.ErrMissing
	}
	return segment.bytes.Read(ctx, local)
}

func (w *structuralSegmentWriter) Seal() error {
	_, err := w.freeze(false)
	return err
}

func (w *structuralSegmentWriter) Freeze() (map[uint32]*structuralSegment, error) {
	return w.freeze(true)
}

func (w *structuralSegmentWriter) freeze(retain bool) (map[uint32]*structuralSegment, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil, errStructuralSegmentsClosed
	}
	retained := make(map[uint32]*structuralSegment, len(w.segments))
	for id, segment := range w.segments {
		if err := segment.bytes.Seal(); err != nil {
			for _, retainedSegment := range retained {
				retainedSegment.release()
			}
			return nil, err
		}
		if retain {
			segment.retain()
			retained[id] = segment
		}
	}
	w.current = nil
	w.currentID = 0
	if !retain {
		return nil, nil
	}
	return retained, nil
}

func (w *structuralSegmentWriter) TotalBytes() int64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.total
}

func (w *structuralSegmentWriter) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	segments := w.segments
	w.segments = nil
	w.current = nil
	w.currentID = 0
	w.mu.Unlock()
	for _, segment := range segments {
		segment.release()
	}
	return nil
}
