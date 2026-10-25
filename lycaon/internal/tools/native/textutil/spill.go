package textutil

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
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

// DirectorySpills keeps runs as files in a host-owned directory that no
// program operand addresses.
type DirectorySpills struct{ Dir string }

var _ SpillStorage = DirectorySpills{}

func (d DirectorySpills) Create(ctx context.Context) (SpillRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(d.Dir, "sort-run-*")
	if err != nil {
		return nil, err
	}
	return &fileRun{path: file.Name(), file: file, writer: bufio.NewWriterSize(file, 64<<10)}, nil
}

type fileRun struct {
	path   string
	file   *os.File
	writer *bufio.Writer
	frame  [binary.MaxVarintLen64]byte
}

func (r *fileRun) WriteRecord(record []byte) error {
	n := binary.PutUvarint(r.frame[:], uint64(len(record)))
	if _, err := r.writer.Write(r.frame[:n]); err != nil {
		return err
	}
	_, err := r.writer.Write(record)
	return err
}

func (r *fileRun) Finish() error {
	if r.file == nil {
		return nil
	}
	err := r.writer.Flush()
	if closeErr := r.file.Close(); err == nil {
		err = closeErr
	}
	r.file = nil
	return err
}

func (r *fileRun) Open(maximumRecord int64, grow func(int64) error) (RunReader, error) {
	if err := grow(64 << 10); err != nil {
		return nil, err
	}
	file, err := os.Open(r.path)
	if err != nil {
		return nil, err
	}
	return &fileRunReader{file: file, reader: bufio.NewReaderSize(file, 64<<10), maximum: maximumRecord, grow: grow}, nil
}

func (r *fileRun) Remove() error {
	_ = r.Finish()
	if err := os.Remove(r.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

type fileRunReader struct {
	file    *os.File
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
	if size > uint64(r.maximum) {
		return nil, nativeutility.LimitExceeded("record_bytes", r.maximum, int64(min(size, 1<<62)))
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

func (r *fileRunReader) Close() error { return r.file.Close() }

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

// spillRootName is the private directory under the session TMPDIR that holds
// every program's sort runs; no program operand names it.
const spillRootName = ".painted-wolf-sort-spills"

// SessionSpills holds one program's sort runs in a private directory under
// its session's TMPDIR. Byte quotas stay with the program's SpillAdmission;
// this owner only places and removes runs. Close removes the program's
// directory when the program settles; ReclaimSessionSpills removes every
// program's leftovers for a session after host loss.
type SessionSpills struct {
	mu     sync.Mutex
	dir    string
	closed bool
}

var _ SpillStorage = (*SessionSpills)(nil)

// NewSessionSpills is the program runtime's one-line wiring: one value per
// program, closed when the program settles.
func NewSessionSpills(tc tools.ToolContext, programID string) (*SessionSpills, error) {
	scratch := strings.TrimSpace(tc.Host.SessionScratchDir)
	if scratch == "" {
		return nil, tools.ErrSessionScratchUnavailable
	}
	if programID == "" || filepath.Base(programID) != programID || strings.HasPrefix(programID, ".") {
		return nil, errors.New("sort spills require a program identity")
	}
	return &SessionSpills{dir: filepath.Join(scratch, spillRootName, programID)}, nil
}

// Create opens a new run, creating the program's directory on first use.
func (s *SessionSpills) Create(ctx context.Context) (SpillRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("program sort spills are closed")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	return DirectorySpills{Dir: s.dir}.Create(ctx)
}

// Close removes every run of the program, including runs an interrupted
// operation left behind, and refuses later runs.
func (s *SessionSpills) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return os.RemoveAll(s.dir)
}

// ReclaimSessionSpills removes all programs' sort runs for a session. The
// host calls it after supervisor loss, when no program of the session runs.
func ReclaimSessionSpills(tc tools.ToolContext) error {
	scratch := strings.TrimSpace(tc.Host.SessionScratchDir)
	if scratch == "" {
		return nil
	}
	return os.RemoveAll(filepath.Join(scratch, spillRootName))
}
