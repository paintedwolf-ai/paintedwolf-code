package textutil

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"github.com/lycaon/lycaon/internal/tools"
	"io"
	"sync"

	"github.com/lycaon/lycaon/internal/scratch"
)

// SpillStorage creates operation-private sort runs. Runs are never published
// or read by anything but the merge, and each is removed when its operation
// ends, completed or not.
type SpillStorage interface {
	Create(context.Context) (SpillRun, error)
}

// SpillRun is one sorted run of length-framed records, so records may hold
// any bytes, including delimiters and NUL.
type SpillRun interface {
	WriteRecord([]byte) error
	Finish() error
	// Open reads the run back; grow admits record buffer growth first.
	Open(maximumRecord int64, grow func(int64) error) (RunReader, error)
	Remove() error
}

type RunReader interface {
	Next() ([]byte, error)
	Close() error
}

// Spills owns a program's framed sort runs. Physical byte storage belongs to
// scratch; sort admits storage and removes each run after its merge.
type Spills struct {
	mu     sync.Mutex
	runs   map[*fileRun]struct{}
	closed bool
}

func (s *Spills) Create(ctx context.Context) (SpillRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.closed {
		return nil, errors.New("program sort spills are closed")
	}
	spool, err := scratch.NewByteSpool()
	if err != nil {
		return nil, err
	}
	run := &fileRun{spool: spool, owner: s, writer: bufio.NewWriterSize(spool, 64<<10)}
	if s.runs == nil {
		s.runs = map[*fileRun]struct{}{}
	}
	s.runs[run] = struct{}{}
	return run, nil
}

// Close releases runs left by interrupted operations and refuses later runs.
func (s *Spills) Close() error {
	s.mu.Lock()
	s.closed = true
	runs := s.runs
	s.runs = nil
	s.mu.Unlock()
	var result error
	for run := range runs {
		result = errors.Join(result, run.Remove())
	}
	return result
}

type fileRun struct {
	spool  *scratch.ByteSpool
	owner  *Spills
	writer *bufio.Writer
	frame  [binary.MaxVarintLen64]byte
}

func (r *fileRun) WriteRecord(record []byte) error {
	if r.writer == nil {
		return io.ErrClosedPipe
	}
	n := binary.PutUvarint(r.frame[:], uint64(len(record)))
	if _, err := r.writer.Write(r.frame[:n]); err != nil {
		return err
	}
	_, err := r.writer.Write(record)
	return err
}

func (r *fileRun) Finish() error {
	if r.writer == nil {
		return nil
	}
	err := r.writer.Flush()
	r.writer = nil
	return err
}

func (r *fileRun) Open(maximumRecord int64, grow func(int64) error) (RunReader, error) {
	if err := grow(64 << 10); err != nil {
		return nil, err
	}
	reader, err := r.spool.Reader()
	if err != nil {
		return nil, err
	}
	return &fileRunReader{reader: bufio.NewReaderSize(reader, 64<<10), maximum: maximumRecord, grow: grow}, nil
}

func (r *fileRun) Remove() error {
	r.owner.mu.Lock()
	delete(r.owner.runs, r)
	r.owner.mu.Unlock()
	r.writer = nil
	return r.spool.Close()
}

type fileRunReader struct {
	reader  *bufio.Reader
	maximum int64
	record  []byte
	grow    func(int64) error
}

func (r *fileRunReader) Next() ([]byte, error) {
	size, err := binary.ReadUvarint(r.reader)
	if err != nil {
		return nil, err
	}
	if r.maximum < 0 || size > uint64(r.maximum) || size > uint64(^uint(0)>>1) {
		return nil, tools.CommandLimitExceeded("record_bytes", r.maximum, int64(min(size, 1<<62)))
	}
	if uint64(cap(r.record)) < size {
		if err := r.grow(int64(size)); err != nil {
			return nil, err
		}
		r.record = make([]byte, size)
	}
	r.record = r.record[:size]
	if _, err := io.ReadFull(r.reader, r.record); err != nil {
		return nil, err
	}
	return r.record, nil
}

func (r *fileRunReader) Close() error { r.reader = nil; r.record = nil; return nil }

type mergeCursor struct {
	reader RunReader
	record []byte
	index  int
}

func (c *mergeCursor) advance() (bool, error) {
	record, err := c.reader.Next()
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	c.record = record
	return err == nil, err
}

// mergeHeap orders cursors by record and then by run index, which keeps the
// merge stable across runs spilled in input order.
type mergeHeap struct {
	order   sortOrder
	cursors []*mergeCursor
}

func (h *mergeHeap) Len() int { return len(h.cursors) }
func (h *mergeHeap) Less(i, j int) bool {
	if c := h.order.compare(h.cursors[i].record, h.cursors[j].record); c != 0 {
		return c < 0
	}
	return h.cursors[i].index < h.cursors[j].index
}
func (h *mergeHeap) Swap(i, j int) { h.cursors[i], h.cursors[j] = h.cursors[j], h.cursors[i] }
func (h *mergeHeap) Push(x any)    { h.cursors = append(h.cursors, x.(*mergeCursor)) }
func (h *mergeHeap) Pop() any {
	last := h.cursors[len(h.cursors)-1]
	h.cursors = h.cursors[:len(h.cursors)-1]
	return last
}
