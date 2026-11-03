package scratch

import (
	"io"
	"os"
	"sync"
)

// ByteSpool holds transient bytes in an anonymous file. Callers admit storage
// before writing and retain the spool until all snapshot readers have finished.
// Closing the spool invalidates its readers and releases the file.
type ByteSpool struct {
	mu   sync.Mutex
	file *os.File
	size int64
}

// NewByteSpool removes the file's name before any bytes can be written. The OS
// also releases the storage after host loss; no named temporary file exposes raw bytes.
func NewByteSpool() (*ByteSpool, error) {
	file, err := os.CreateTemp("", "painted-wolf-spool-*")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(file.Name()); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, err
	}
	return &ByteSpool{file: file}, nil
}

func (s *ByteSpool) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return 0, os.ErrClosed
	}
	n, err := s.file.Write(p)
	s.size += int64(n)
	return n, err
}

// Reader returns an independent snapshot of the bytes written so far. Later
// appends are not part of the snapshot, even when it has not been read yet.
func (s *ByteSpool) Reader() (*io.SectionReader, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil, os.ErrClosed
	}
	return io.NewSectionReader(s.file, 0, s.size), nil
}

func (s *ByteSpool) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}
