//go:build linux || darwin

// Package mappedfile holds a shared writable mapping of a file in a child process.
//
// A process that serves a mount must not touch that mount's mapped pages, or hold its files open
// when it forks. A page fault cannot be preempted, and a forking thread keeps its scheduler slot
// until the child's exec closes its inherited descriptors, which sends the server a request.
// Either way a stop-the-world waits on a thread that waits on the stopped server.
package mappedfile

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	execpkg "github.com/lycaon/lycaon/internal/exec"
)

const (
	pathEnv = "PW_MAPPED_FILE_PATH"
	sizeEnv = "PW_MAPPED_FILE_SIZE"
	syncCmd = "sync"
	// channelFD is the socket the child inherits from StartDescriptorHelper.
	channelFD = 3
)

// ServeIfChild runs the mapping child and exits when this process was started as one. Call it
// first in TestMain.
func ServeIfChild() {
	path := os.Getenv(pathEnv)
	if path == "" {
		return
	}
	channel := os.NewFile(channelFD, "mapped-file-channel")
	size, err := strconv.Atoi(os.Getenv(sizeEnv))
	if err != nil {
		reply(channel, err)
		os.Exit(2)
	}
	os.Exit(serve(path, size, channel, channel))
}

// File is a mapping held by a child process.
type File struct {
	mu      sync.Mutex
	channel io.WriteCloser
	replies *bufio.Scanner
	wait    func() error
}

// Map starts a child of the test binary that maps the first size bytes of path. The child exits
// when the test ends.
func Map(t testing.TB, path string, size int) *File {
	t.Helper()
	f, err := start(t, path, size)
	if err != nil {
		t.Fatalf("map %s: %v", path, err)
	}
	return f
}

func start(t testing.TB, path string, size int) (*File, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	parent, child, err := socketPair()
	if err != nil {
		return nil, err
	}
	wait, err := execpkg.StartDescriptorHelper(t.Context(), execpkg.HostLaunch("mapped file fixture"), executable, nil,
		[]string{pathEnv + "=" + path, sizeEnv + "=" + strconv.Itoa(size)}, child, os.Stderr)
	// Only the child holds its end now, so its exit ends the parent's reads.
	_ = child.Close()
	if err != nil {
		_ = parent.Close()
		return nil, err
	}
	f := &File{channel: parent, replies: bufio.NewScanner(parent), wait: wait}
	t.Cleanup(f.close)
	if err := f.await(); err != nil {
		return nil, err
	}
	return f, nil
}

// socketPair returns a connected pair that later children do not inherit; ExtraFiles still hands
// the child its end. Holding ForkLock keeps a concurrent fork from copying the ends before then.
func socketPair() (parent, child *os.File, err error) {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, err
	}
	unix.CloseOnExec(fds[0])
	unix.CloseOnExec(fds[1])
	return os.NewFile(uintptr(fds[0]), "mapped-file-parent"), os.NewFile(uintptr(fds[1]), "mapped-file-child"), nil
}

// Write copies data to the start of the mapping and leaves the pages dirty.
func (f *File) Write(data []byte) error { return f.request(hex.EncodeToString(data)) }

// Sync writes the mapping's dirty pages back with msync, then fsyncs the file.
func (f *File) Sync() error { return f.request(syncCmd) }

func (f *File) request(line string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := io.WriteString(f.channel, line+"\n"); err != nil {
		return err
	}
	return f.await()
}

func (f *File) await() error {
	if !f.replies.Scan() {
		return errors.Join(errors.New("mapping child exited"), f.replies.Err())
	}
	if text := f.replies.Text(); text != "ok" {
		return errors.New(strings.TrimPrefix(text, "error "))
	}
	return nil
}

func (f *File) close() {
	_ = f.channel.Close()
	_ = f.wait()
}

func serve(path string, size int, in io.Reader, out io.Writer) int {
	file, err := os.OpenFile(path, os.O_RDWR, 0) // #nosec G703 -- the parent test names its own fixture file.
	if err != nil {
		reply(out, err)
		return 1
	}
	defer func() { _ = file.Close() }()
	mapped, err := unix.Mmap(int(file.Fd()), 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		reply(out, err)
		return 1
	}
	defer func() { _ = unix.Munmap(mapped) }()
	reply(out, nil)
	commands := bufio.NewScanner(in)
	for commands.Scan() {
		if commands.Text() == syncCmd {
			err := unix.Msync(mapped, unix.MS_SYNC)
			if err == nil {
				err = file.Sync()
			}
			reply(out, err)
			continue
		}
		reply(out, write(mapped, commands.Text()))
	}
	return 0
}

func write(mapped []byte, encoded string) error {
	data, err := hex.DecodeString(encoded)
	if err != nil {
		return err
	}
	if len(data) > len(mapped) {
		return fmt.Errorf("%d bytes do not fit a %d-byte mapping", len(data), len(mapped))
	}
	copy(mapped, data)
	return nil
}

func reply(out io.Writer, err error) {
	if err != nil {
		_, _ = fmt.Fprintf(out, "error %v\n", err)
		return
	}
	_, _ = fmt.Fprintln(out, "ok")
}
