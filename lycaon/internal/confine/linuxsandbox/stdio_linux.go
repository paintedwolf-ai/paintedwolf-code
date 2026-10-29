//go:build linux

package linuxsandbox

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// StreamError refuses a handover or reports bytes that could not be delivered.
type StreamError struct {
	Stream    int
	Operation string
	Err       error
}

func (e *StreamError) Error() string {
	return fmt.Sprintf("standard stream %d %s: %v", e.Stream, e.Operation, e.Err)
}
func (e *StreamError) Unwrap() error { return e.Err }

type standardBridge struct {
	stream   int
	original *os.File
	child    *os.File
	copyEnd  *os.File
	done     chan struct{}
	stopping atomic.Bool
	err      error
}

type standardStreams struct {
	files   [3]*os.File
	bridges []*standardBridge
	settled sync.Once
	result  error
}

// handoverStandardStreams owns regular-file copies until the helper has reaped
// its target and descendants. Pipes, terminals, sockets and character devices
// keep their descriptors; unsupported descriptor kinds refuse. The helper's
// standard descriptors are the target's streams, so they are taken by number.
func handoverStandardStreams(cmd *exec.Cmd) (func() error, error) {
	standard := [3]*os.File{os.NewFile(0, "stdin"), os.NewFile(1, "stdout"), os.NewFile(2, "stderr")}
	streams, err := newStandardStreams(standard, sameOpenFile)
	if err != nil {
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = streams.files[0], streams.files[1], streams.files[2]
	return streams.finish, nil
}

func newStandardStreams(files [3]*os.File, query func(*os.File, *os.File) (bool, error)) (_ *standardStreams, resultErr error) {
	streams := &standardStreams{files: files}
	defer func() {
		if resultErr != nil {
			for _, bridge := range streams.bridges {
				bridge.close()
			}
		}
	}()
	// Inspect all three before creating anything: a closed standard slot may be
	// reused by a pipe, but must still be closed in the child.
	var stats [3]unix.Stat_t
	var closed [3]bool
	for i, file := range files {
		if file == nil {
			closed[i] = true
			continue
		}
		err := unix.Fstat(int(file.Fd()), &stats[i])
		if errors.Is(err, unix.EBADF) {
			closed[i] = true
			continue
		}
		if err != nil {
			return nil, &StreamError{i, "inspect", err}
		}
		switch stats[i].Mode & unix.S_IFMT {
		case unix.S_IFREG, unix.S_IFIFO, unix.S_IFSOCK, unix.S_IFCHR:
		default:
			return nil, &StreamError{i, "unsupported descriptor kind", unix.EINVAL}
		}
	}
	sharedOutput := false
	if stats[1].Mode&unix.S_IFMT == unix.S_IFREG && stats[2].Mode&unix.S_IFMT == unix.S_IFREG && stats[1].Dev == stats[2].Dev && stats[1].Ino == stats[2].Ino {
		same, err := query(files[1], files[2])
		if err != nil {
			return nil, &StreamError{2, "compare shared output", err}
		}
		sharedOutput = same
	}
	for i, file := range files {
		if closed[i] {
			read, write, err := os.Pipe()
			if err != nil {
				return nil, &StreamError{i, "prepare closed descriptor", err}
			}
			_ = read.Close()
			_ = write.Close()
			streams.files[i] = read
			continue
		}
		if i == 2 && sharedOutput {
			streams.files[2] = streams.files[1]
			continue
		}
		if stats[i].Mode&unix.S_IFMT != unix.S_IFREG {
			continue
		}
		flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
		if err != nil {
			return nil, &StreamError{i, "inspect access", err}
		}
		if (i == 0 && flags&unix.O_ACCMODE == unix.O_WRONLY) || (i != 0 && flags&unix.O_ACCMODE == unix.O_RDONLY) {
			return nil, &StreamError{i, "access", unix.EBADF}
		}
		fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 3)
		if err != nil {
			return nil, &StreamError{i, "retain", err}
		}
		original := os.NewFile(uintptr(fd), "standard-stream-source")
		read, write, err := os.Pipe()
		if err != nil {
			_ = original.Close()
			return nil, &StreamError{i, "pipe", err}
		}
		bridge := &standardBridge{stream: i, original: original, child: write, copyEnd: read, done: make(chan struct{})}
		if i == 0 {
			bridge.child, bridge.copyEnd = read, write
		}
		streams.bridges = append(streams.bridges, bridge)
		streams.files[i] = bridge.child
	}
	for _, bridge := range streams.bridges {
		go bridge.copy()
	}
	return streams, nil
}

// Kernel open-file-description equality preserves 1/2 ordering without merging
// two independent opens of the same inode and their independent offsets.
func sameOpenFile(a, b *os.File) (bool, error) {
	answer, err := unix.FcntlInt(a.Fd(), unix.F_DUPFD_QUERY, int(b.Fd()))
	if err == nil {
		return answer == 1, nil
	}
	if !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOSYS) {
		return false, err
	}
	const kcmpFile = 0
	result, _, errno := unix.Syscall6(unix.SYS_KCMP, uintptr(os.Getpid()), uintptr(os.Getpid()), kcmpFile, a.Fd(), b.Fd(), 0)
	if errno != 0 {
		return false, errno
	}
	return result == 0, nil
}

func (b *standardBridge) copy() {
	defer close(b.done)
	var from io.Reader = b.copyEnd
	var to io.Writer = b.original
	if b.stream == 0 {
		from, to = b.original, b.copyEnd
	}
	// The fixed copy block and kernel pipe bound memory and read-ahead.
	buffer := make([]byte, 32*1024)
	err := copyStream(to, from, buffer, b.stopping.Load)
	if b.stopping.Load() && (errors.Is(err, os.ErrClosed) || errors.Is(err, unix.EPIPE) || errors.Is(err, os.ErrDeadlineExceeded)) {
		err = nil
	}
	if err != nil {
		b.err = &StreamError{b.stream, "copy", err}
	}
	if b.stream == 0 || err != nil {
		_ = b.copyEnd.Close()
	}
}

func copyStream(to io.Writer, from io.Reader, buffer []byte, stopped func() bool) error {
	for {
		if stopped() {
			return nil
		}
		n, readErr := from.Read(buffer)
		for written := 0; written < n; {
			count, err := to.Write(buffer[written:n])
			written += count
			if err != nil {
				return err
			}
			if count == 0 {
				return io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (s *standardStreams) finish() error {
	s.settled.Do(func() { s.result = s.settle() })
	return s.result
}

func (s *standardStreams) settle() error {
	var result error
	// A recipient outside the process tree may retain a writer. End copying at
	// target settlement, then drain only the bytes already queued in each pipe.
	for _, b := range s.bridges {
		b.stopping.Store(true)
		if b.stream == 0 {
			_ = b.original.Close()
			_ = b.copyEnd.Close()
		} else {
			select {
			case <-b.done:
			default:
				if err := b.copyEnd.SetReadDeadline(time.Now()); err != nil {
					result = errors.Join(result, &StreamError{b.stream, "stop output read", err})
					_ = b.copyEnd.Close()
				}
			}
		}
		_ = b.child.Close()
	}
	for _, b := range s.bridges {
		<-b.done
		result = errors.Join(b.err, result)
		if b.stream != 0 && b.err == nil {
			if err := b.drainQueued(); err != nil {
				result = errors.Join(result, &StreamError{b.stream, "drain output", err})
			}
		}
		b.close()
	}
	return result
}

func (b *standardBridge) drainQueued() error {
	if err := b.copyEnd.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	queued, err := unix.IoctlGetInt(int(b.copyEnd.Fd()), unix.TIOCINQ)
	if err != nil {
		return err
	}
	return copyStream(b.original, io.LimitReader(b.copyEnd, int64(queued)), make([]byte, 32*1024), func() bool { return false })
}

func (b *standardBridge) close() { _ = b.child.Close(); _ = b.copyEnd.Close(); _ = b.original.Close() }
