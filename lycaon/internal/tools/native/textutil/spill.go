package textutil

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"

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
