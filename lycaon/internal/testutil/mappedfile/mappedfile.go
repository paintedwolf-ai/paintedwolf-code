//go:build linux || darwin

// Package mappedfile holds a shared writable mapping of a file in a child process.
//
// A process that serves a mount must not touch that mount's mapped pages itself. A page fault
// cannot be preempted, so a stop-the-world waits on the faulting thread while the server
// goroutines that would answer the fault are already stopped.
package mappedfile

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

const (
	pathEnv = "PW_MAPPED_FILE_PATH"
	sizeEnv = "PW_MAPPED_FILE_SIZE"
	syncCmd = "sync"
)

// ServeIfChild runs the mapping child and exits when this process was started as one. Call it
// first in TestMain.
func ServeIfChild() {
	path := os.Getenv(pathEnv)
	if path == "" {
		return
	}
	size, err := strconv.Atoi(os.Getenv(sizeEnv))
	if err != nil {
		reply(os.Stdout, err)
		os.Exit(2)
	}
	os.Exit(serve(path, size, os.Stdin, os.Stdout))
}

// File is a mapping held by a child process.
type File struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	replies *bufio.Scanner
}

// Map starts a child of the test binary that maps the first size bytes of path. The child exits
// when the test ends.
func Map(t testing.TB, path string, size int) *File {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), pathEnv+"="+path, sizeEnv+"="+strconv.Itoa(size))
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("mapping child stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("mapping child stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start mapping child: %v", err)
	}
	f := &File{cmd: cmd, stdin: stdin, replies: bufio.NewScanner(stdout)}
	t.Cleanup(f.close)
	if err := f.await(); err != nil {
		t.Fatalf("map %s: %v", path, err)
	}
	return f
}

// Write copies data to the start of the mapping and leaves the pages dirty.
func (f *File) Write(data []byte) error { return f.request(hex.EncodeToString(data)) }

// Sync writes the mapping's dirty pages back with msync.
func (f *File) Sync() error { return f.request(syncCmd) }

func (f *File) request(line string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := io.WriteString(f.stdin, line+"\n"); err != nil {
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
	_ = f.stdin.Close()
	_ = f.cmd.Wait()
}

func serve(path string, size int, in io.Reader, out io.Writer) int {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
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
			reply(out, unix.Msync(mapped, unix.MS_SYNC))
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
