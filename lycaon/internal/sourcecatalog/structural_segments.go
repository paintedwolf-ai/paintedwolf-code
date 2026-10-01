package sourcecatalog

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"math"
	"os"
	"sort"
	"sync"
	"sync/atomic"
)

const (
	structuralSegmentChunkSize = 1 << 20
	structuralRecordHeaderSize = 12
	maxStructuralSegmentEntry  = 64 << 20
	structuralSegmentsMaxBytes = int64(structuralOffsetMask)
	structuralResidentLimit    = 128 << 20
	structuralSegmentReadBlock = 64 << 10
)

var structuralResidentBytes atomic.Int64
var structuralEncodedBytes atomic.Int64

// A spill has no path, so the block cache keys on this serial instead.
var structuralSpillSerial atomic.Uint64

// structuralSpilledBytes is how much encoded structure lives in spill files.
var structuralSpilledBytes atomic.Int64

// SpilledBytes reports disk held by spill files. They have no directory entry,
// so a size walk of the cache tree misses them.
func SpilledBytes() int64 { return structuralSpilledBytes.Load() }

var (
	errStructuralSegmentsClosed = errors.New("structural segments are closed")
	errStructuralSegmentsSealed = errors.New("structural segments are sealed")
	errStructuralSegmentID      = errors.New("invalid structural segment id")
	errStructuralSegmentAddress = errors.New("structural segment address space exhausted")
)

type structuralSegmentChunk struct {
	start int64
	body  []byte
}

// structuralSegments is one append-only byte store. Record IDs are byte
// offsets plus one, so a large generation does not retain an offset per entry.
type structuralSegments struct {
	mu          sync.RWMutex
	dir         string
	memoryLimit int64
	chunks      []structuralSegmentChunk
	spill       *os.File
	spillID     uint64
	spillBuffer []byte
	spillOffset int64
	bytes       int64
	resident    int64
	sealed      bool
	closed      bool
}

func newStructuralSegments(dir string, memoryLimit int64) (*structuralSegments, error) {
	if dir == "" || memoryLimit < 0 {
		return nil, os.ErrInvalid
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &structuralSegments{dir: dir, memoryLimit: memoryLimit}, nil
}

func (s *structuralSegments) Append(ctx context.Context, body []byte) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(body) > maxStructuralSegmentEntry {
		return 0, os.ErrInvalid
	}
	record := make([]byte, structuralRecordHeaderSize+len(body))
	binary.LittleEndian.PutUint64(record[:8], uint64(len(body)))
	binary.LittleEndian.PutUint32(record[8:12], crc32.ChecksumIEEE(body))
	copy(record[structuralRecordHeaderSize:], body)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errStructuralSegmentsClosed
	}
	if s.sealed {
		return 0, errStructuralSegmentsSealed
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	offset := s.bytes
	if offset < 0 || offset > structuralSegmentsMaxBytes-int64(len(record)) {
		return 0, errStructuralSegmentAddress
	}
	if !reserveStructuralEncoded(int64(len(record))) {
		return 0, errStructuralSegmentAddress
	}
	committed := false
	defer func() {
		if !committed {
			structuralEncodedBytes.Add(-int64(len(record)))
		}
	}()
	id := uint64(offset) + 1
	if s.spill == nil && !s.appendMemoryLocked(record) {
		if err := s.startSpillLocked(); err != nil {
			return 0, err
		}
	}
	if s.spill != nil {
		if err := s.appendSpillLocked(record); err != nil {
			return 0, err
		}
		structuralSpilledBytes.Add(int64(len(record)))
	}
	s.bytes += int64(len(record))
	committed = true
	return id, nil
}

func (s *structuralSegments) appendMemoryLocked(record []byte) bool {
	if len(s.chunks) > 0 {
		last := &s.chunks[len(s.chunks)-1]
		if cap(last.body)-len(last.body) >= len(record) {
			last.body = append(last.body, record...)
			return true
		}
	}
	capacity := int64(structuralSegmentChunkSize)
	if int64(len(record)) > capacity {
		capacity = int64(len(record))
	}
	if remaining := s.memoryLimit - s.resident; remaining < capacity {
		capacity = remaining
	}
	if capacity < int64(len(record)) || !reserveStructuralResident(capacity) {
		return false
	}
	s.resident += capacity
	s.chunks = append(s.chunks, structuralSegmentChunk{start: s.bytes, body: make([]byte, 0, int(capacity))})
	last := &s.chunks[len(s.chunks)-1]
	last.body = append(last.body, record...)
	return true
}

// The spill is unlinked at creation, so its descriptor is the only handle and a
// killed process leaves nothing behind. That descriptor lives as long as the
// segment, so open spills grow with the chain until compaction collapses it.
func (s *structuralSegments) startSpillLocked() error {
	file, err := createStructuralTempFile(s.dir, "structural-segments-*.tmp")
	if err != nil {
		return err
	}
	for _, chunk := range s.chunks {
		if err := writeAll(file, chunk.body); err != nil {
			_ = file.Close()
			return err
		}
	}
	s.spill, s.spillID, s.chunks = file, structuralSpillSerial.Add(1), nil
	structuralSpilledBytes.Add(s.bytes)
	structuralResidentBytes.Add(-s.resident)
	s.resident = 0
	return nil
}

// Buffered append keeps page-sized writes off the filesystem syscall path.
func (s *structuralSegments) appendSpillLocked(record []byte) error {
	if s.spillBuffer == nil {
		capacity := min(int64(structuralSegmentChunkSize), s.memoryLimit)
		if capacity >= int64(len(record)) && reserveStructuralResident(capacity) {
			s.spillBuffer = make([]byte, 0, int(capacity))
			s.resident += capacity
		}
	}
	if len(s.spillBuffer)+len(record) > cap(s.spillBuffer) {
		if err := writeAllAt(s.spill, s.spillBuffer, s.spillOffset); err != nil {
			return err
		}
		s.spillBuffer = s.spillBuffer[:0]
	}
	if len(s.spillBuffer) == 0 {
		s.spillOffset = s.bytes
	}
	if len(record) > cap(s.spillBuffer) {
		if err := writeAllAt(s.spill, record, s.bytes); err != nil {
			return err
		}
		s.spillOffset += int64(len(record))
		return nil
	}
	s.spillBuffer = append(s.spillBuffer, record...)
	return nil
}

func reserveStructuralResident(bytes int64) bool {
	for {
		used := structuralResidentBytes.Load()
		if bytes > structuralResidentLimit-used {
			return false
		}
		if structuralResidentBytes.CompareAndSwap(used, used+bytes) {
			return true
		}
	}
}

func reserveStructuralEncoded(bytes int64) bool {
	if bytes < 0 {
		return false
	}
	for {
		used := structuralEncodedBytes.Load()
		if used > math.MaxInt64-bytes {
			return false
		}
		if structuralEncodedBytes.CompareAndSwap(used, used+bytes) {
			return true
		}
	}
}

func (s *structuralSegments) Read(ctx context.Context, id uint64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id == 0 || id > uint64(^uint64(0)>>1) {
		return nil, errStructuralSegmentID
	}
	offset := int64(id - 1)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, errStructuralSegmentsClosed
	}
	if offset < 0 || offset+structuralRecordHeaderSize > s.bytes {
		return nil, errStructuralSegmentID
	}
	header := make([]byte, structuralRecordHeaderSize)
	if err := s.readAtLocked(ctx, header, offset); err != nil {
		return nil, err
	}
	length := binary.LittleEndian.Uint64(header[:8])
	if length > maxStructuralSegmentEntry {
		return nil, errStructuralSegmentID
	}
	if int64(length) > s.bytes-offset-structuralRecordHeaderSize {
		return nil, errStructuralSegmentID
	}
	body := make([]byte, int(length))
	if err := s.readAtLocked(ctx, body, offset+structuralRecordHeaderSize); err != nil {
		return nil, err
	}
	if crc32.ChecksumIEEE(body) != binary.LittleEndian.Uint32(header[8:12]) {
		return nil, errors.New("structural segment checksum mismatch")
	}
	return body, nil
}

func (s *structuralSegments) readAtLocked(ctx context.Context, dst []byte, offset int64) error {
	if len(dst) == 0 {
		return nil
	}
	if s.spill != nil {
		if offset < s.spillOffset {
			amount := min(int64(len(dst)), s.spillOffset-offset)
			if err := readStructuralSpillBlockCached(ctx, s.spill, s.spillID, dst[:amount], offset, s.spillOffset); err != nil {
				return err
			}
			dst, offset = dst[amount:], offset+amount
		}
		if len(dst) == 0 {
			return nil
		}
		start := offset - s.spillOffset
		if start < 0 || start > int64(len(s.spillBuffer)) || int64(len(dst)) > int64(len(s.spillBuffer))-start {
			return io.ErrUnexpectedEOF
		}
		copy(dst, s.spillBuffer[start:])
		return nil
	}
	remaining := dst
	position := offset
	first := sort.Search(len(s.chunks), func(i int) bool {
		return s.chunks[i].start+int64(len(s.chunks[i].body)) > position
	})
	for _, chunk := range s.chunks[first:] {
		end := chunk.start + int64(len(chunk.body))
		if position >= end {
			continue
		}
		if position < chunk.start {
			return io.ErrUnexpectedEOF
		}
		start := int(position - chunk.start)
		copied := copy(remaining, chunk.body[start:])
		remaining = remaining[copied:]
		position += int64(copied)
		if len(remaining) == 0 {
			return nil
		}
	}
	return io.ErrUnexpectedEOF
}

func (s *structuralSegments) Seal() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errStructuralSegmentsClosed
	}
	if err := s.flushSpillLocked(); err != nil {
		return err
	}
	// A sealed spill keeps its descriptor, the only handle to an unlinked file.
	// Its append buffer is dead, so that reservation returns to the shared pool.
	// An unspilled segment still holds its chunks, so its reservation stands.
	if s.spill != nil && s.spillBuffer != nil {
		structuralResidentBytes.Add(-s.resident)
		s.resident = 0
		s.spillBuffer = nil
	}
	s.sealed = true
	return nil
}

func (s *structuralSegments) flushSpillLocked() error {
	if s.spill == nil || len(s.spillBuffer) == 0 {
		return nil
	}
	if err := writeAllAt(s.spill, s.spillBuffer, s.spillOffset); err != nil {
		return err
	}
	s.spillOffset += int64(len(s.spillBuffer))
	s.spillBuffer = s.spillBuffer[:0]
	return nil
}

func (s *structuralSegments) Bytes() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bytes
}

func (s *structuralSegments) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	file := s.spill
	spillID := s.spillID
	resident := s.resident
	encoded := s.bytes
	s.resident = 0
	s.bytes = 0
	s.spill, s.spillID, s.chunks = nil, 0, nil
	s.spillBuffer = nil
	s.mu.Unlock()
	structuralResidentBytes.Add(-resident)
	structuralEncodedBytes.Add(-encoded)
	if file == nil {
		return nil
	}
	// Closing the last descriptor is what reclaims the bytes of an unlinked file.
	structuralSpilledBytes.Add(-encoded)
	clearStructuralSpillBlocks(spillID)
	return file.Close()
}

func writeAll(writer io.Writer, body []byte) error {
	for len(body) > 0 {
		n, err := writer.Write(body)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		body = body[n:]
	}
	return nil
}

func writeAllAt(writer io.WriterAt, body []byte, offset int64) error {
	for len(body) > 0 {
		n, err := writer.WriteAt(body, offset)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		body = body[n:]
		offset += int64(n)
	}
	return nil
}

func readAllAt(reader io.ReaderAt, body []byte, offset int64) error {
	for len(body) > 0 {
		n, err := reader.ReadAt(body, offset)
		if n > 0 {
			body = body[n:]
			offset += int64(n)
		}
		if len(body) == 0 {
			return nil
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
	}
	return nil
}
