// Package commandspill owns private, length-framed sort storage and its program lifecycle.
package commandspill

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	if size > math.MaxInt64 {
		return nil, &RecordLimitError{Maximum: r.maximum, Observed: 1 << 62}
	}
	recordSize := int64(size)
	if recordSize > r.maximum {
		return nil, &RecordLimitError{Maximum: r.maximum, Observed: min(recordSize, 1<<62)}
	}
	if uint64(cap(r.record)) < size {
		if err := r.grow(recordSize); err != nil {
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

// NewSessionSpills binds one program to its host-owned session scratch directory.
func NewSessionSpills(scratch, programID string) (*SessionSpills, error) {
	scratch = strings.TrimSpace(scratch)
	if scratch == "" {
		return nil, ErrScratchUnavailable
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
func ReclaimSessionSpills(scratch string) error {
	scratch = strings.TrimSpace(scratch)
	if scratch == "" {
		return nil
	}
	return os.RemoveAll(filepath.Join(scratch, spillRootName))
}

// ErrScratchUnavailable means this session has no private spill directory.
var ErrScratchUnavailable = errors.New("session scratch unavailable")

// RecordLimitError reports a frame rejected before allocation or payload reads.
type RecordLimitError struct{ Maximum, Observed int64 }

func (e *RecordLimitError) Error() string {
	return fmt.Sprintf("spill record exceeds limit: %d > %d", e.Observed, e.Maximum)
}
