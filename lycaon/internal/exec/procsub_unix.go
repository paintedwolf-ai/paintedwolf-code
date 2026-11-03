//go:build unix

package exec

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// ProcessSubstitutionStream is an active <(list) or >(list) byte stream to be
// materialized as a named pipe (FIFO) for a confined external stage.
type ProcessSubstitutionStream struct {
	Reader io.ReadCloser
	Writer io.WriteCloser
}

// BindProcessSubstitutions materializes one named pipe (FIFO) in scratchDir
// per stream, starts background relays between each FIFO and its in-memory
// stream, and returns the FIFO paths in stream order along with a deterministic
// cleanup function.
func BindProcessSubstitutions(scratchDir string, streams []ProcessSubstitutionStream) ([]string, func(), error) {
	if len(streams) == 0 {
		return nil, func() {}, nil
	}
	baseDir := strings.TrimSpace(scratchDir)
	if baseDir != "" {
		if err := os.MkdirAll(baseDir, 0o700); err != nil {
			return nil, nil, err
		}
		if resolved, err := filepath.EvalSymlinks(baseDir); err == nil && resolved != "" {
			baseDir = resolved
		}
	}
	dir, err := os.MkdirTemp(baseDir, "pw-procsub-*")
	if err != nil {
		return nil, nil, err
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != "" {
		dir = resolved
	}
	var closing atomic.Bool
	var wg sync.WaitGroup
	fifoPaths := make([]string, 0, len(streams))
	for i, s := range streams {
		fifoPath := filepath.Join(dir, fmt.Sprintf("fd-%d", i))
		if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
			closing.Store(true)
			for _, u := range streams[:i] {
				if u.Reader != nil {
					_ = u.Reader.Close()
				}
			}
			for _, p := range fifoPaths {
				wakeFIFO(p)
			}
			wg.Wait()
			_ = os.RemoveAll(dir)
			return nil, nil, err
		}
		fifoPaths = append(fifoPaths, fifoPath)
		wg.Add(1)
		go func(s ProcessSubstitutionStream, fifoPath string) {
			defer wg.Done()
			if s.Reader != nil {
				defer func() { _ = s.Reader.Close() }()
				f, err := openFIFO(fifoPath, unix.O_WRONLY)
				if err != nil {
					return
				}
				if !closing.Load() {
					_, _ = io.Copy(f, s.Reader)
				}
				_ = f.Close()
				return
			}
			if s.Writer != nil {
				defer func() { _ = s.Writer.Close() }()
				f, err := openFIFO(fifoPath, unix.O_RDONLY)
				if err != nil {
					return
				}
				_, _ = io.Copy(s.Writer, f)
				_ = f.Close()
			}
		}(s, fifoPath)
	}
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			closing.Store(true)
			for _, u := range streams {
				if u.Reader != nil {
					_ = u.Reader.Close()
				}
			}
			for _, p := range fifoPaths {
				wakeFIFO(p)
			}
			wg.Wait()
			_ = os.RemoveAll(dir)
		})
	}
	return fifoPaths, cleanup, nil
}

func openFIFO(path string, mode int) (*os.File, error) {
	for {
		fd, err := unix.Open(path, mode|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), path), nil
	}
}

func wakeFIFO(path string) {
	if fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK, 0); err == nil {
		_ = unix.Close(fd)
	}
}
