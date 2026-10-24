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

// ProcessSubstitutionStream is an active <(list) or >(list) byte stream bound
// to a virtual descriptor path (/dev/fd/N).
type ProcessSubstitutionStream struct {
	Path   string
	Reader io.ReadCloser
	Writer io.WriteCloser
}

// BindProcessSubstitutions materializes named pipes (FIFOs) in scratchDir for
// any process-substitution paths referenced in argv, starts background relays
// between each FIFO and its in-memory stream, and returns the rewritten argv
// along with a deterministic cleanup function.
func BindProcessSubstitutions(scratchDir string, argv []string, streams []ProcessSubstitutionStream) ([]string, func(), error) {
	if len(streams) == 0 || len(argv) <= 1 {
		return argv, func() {}, nil
	}
	used := make([]ProcessSubstitutionStream, 0, len(streams))
	for _, s := range streams {
		if s.Path == "" {
			continue
		}
		for _, arg := range argv[1:] {
			if strings.Contains(arg, s.Path) {
				used = append(used, s)
				break
			}
		}
	}
	if len(used) == 0 {
		return argv, func() {}, nil
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
	outArgv := append([]string(nil), argv...)
	var closing atomic.Bool
	var wg sync.WaitGroup
	fifoPaths := make([]string, 0, len(used))
	for i, s := range used {
		fifoPath := filepath.Join(dir, fmt.Sprintf("fd-%d", i))
		if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
			closing.Store(true)
			for _, u := range used[:i] {
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
		for j := 1; j < len(outArgv); j++ {
			if strings.Contains(outArgv[j], s.Path) {
				outArgv[j] = strings.ReplaceAll(outArgv[j], s.Path, fifoPath)
			}
		}
		wg.Add(1)
		go func(s ProcessSubstitutionStream, fifoPath string) {
			defer wg.Done()
			if s.Reader != nil {
				defer s.Reader.Close()
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
				defer s.Writer.Close()
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
			for _, u := range used {
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
	return outArgv, cleanup, nil
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
