package debugretention

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// File is an append writer that starts a fresh valid segment at its byte cap.
type File struct {
	mu       sync.Mutex
	file     *os.File
	size     int64
	maxBytes int64
}

// OpenFile opens a capped diagnostic append stream.
func OpenFile(path string, maxBytes int64) (*File, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("debug log path is empty")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultConfig().MaxFileBytes
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create debug log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- configured capture path
	if err != nil {
		return nil, fmt.Errorf("open debug log: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat debug log: %w", err)
	}
	return &File{file: f, size: info.Size(), maxBytes: maxBytes}, nil
}

func (f *File) Write(p []byte) (int, error) {
	if f == nil {
		return 0, os.ErrInvalid
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return 0, os.ErrClosed
	}
	if int64(len(p)) > f.maxBytes {
		return 0, fmt.Errorf("debug record exceeds %d-byte file cap", f.maxBytes)
	}
	if f.size+int64(len(p)) > f.maxBytes {
		if err := f.file.Truncate(0); err != nil {
			return 0, err
		}
		if _, err := f.file.Seek(0, 0); err != nil {
			return 0, err
		}
		f.size = 0
	}
	n, err := f.file.Write(p)
	f.size += int64(n)
	return n, err
}

func (f *File) Close() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return nil
	}
	err := f.file.Close()
	f.file = nil
	return err
}
