//go:build linux

package fuselinux_test

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter/fuselinux"
)

func TestFIFOAndUnixSocketWorkInTheMountAndStayOutOfCheckpoints(t *testing.T) {
	v := prepareMountedView(t, fuselinux.Config{}, "view-special", map[string]string{"seed.txt": "x"})

	fifo := filepath.Join(v.mnt, "pipe")
	if err := syscall.Mkfifo(fifo, 0o640); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	info, err := os.Lstat(fifo)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 || info.Mode().Perm() != 0o640 {
		t.Fatalf("fifo attributes: %v err=%v", info, err)
	}
	if out := v.shell(t, `(printf piped > pipe &) ; cat pipe`); out != "piped" {
		t.Fatalf("fifo carried %q", out)
	}

	shortDir, err := os.MkdirTemp("", "fl") //nolint:usetesting // Kernel Unix addresses are bounded.
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shortDir) })
	if err := os.Symlink(v.mnt, filepath.Join(shortDir, "m")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	sock := filepath.Join(shortDir, "m", "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("bind in mount: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		if c, aErr := ln.Accept(); aErr == nil {
			_, _ = c.Write([]byte("hi"))
			_ = c.Close()
		}
	}()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("connect in mount: %v", err)
	}
	buf := make([]byte, 2)
	if _, err := conn.Read(buf); err != nil || string(buf) != "hi" {
		t.Fatalf("socket carried %q err=%v", buf, err)
	}
	_ = conn.Close()
	if info, err := os.Lstat(filepath.Join(v.mnt, "s.sock")); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket attributes: %v err=%v", info, err)
	}
	entries, err := os.ReadDir(v.mnt)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	types := map[string]os.FileMode{}
	for _, e := range entries {
		types[e.Name()] = e.Type()
	}
	if types["pipe"]&os.ModeNamedPipe == 0 || types["s.sock"]&os.ModeSocket == 0 {
		t.Fatalf("listing types: %v", types)
	}

	changes, err := v.core.EnumerateChanges(true)
	if err != nil {
		t.Fatalf("EnumerateChanges: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("special nodes reached the change set: %+v", changes)
	}
	if err := syscall.Mknod(filepath.Join(v.mnt, "dev"), syscall.S_IFCHR|0o600, 0); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("device node: %v", err)
	}
	if err := os.Remove(fifo); err != nil {
		t.Fatalf("remove fifo: %v", err)
	}
}

func TestDescriptorAttributesReachUnlinkedAndReplacedObjects(t *testing.T) {
	v := prepareMountedView(t, fuselinux.Config{}, "view-setattr-node", map[string]string{"gone": "gone-content", "old": "old-content", "new": "new"})

	gone, err := os.OpenFile(filepath.Join(v.mnt, "gone"), os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open gone: %v", err)
	}
	defer func() { _ = gone.Close() }()
	if err := os.Remove(filepath.Join(v.mnt, "gone")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := gone.Truncate(4); err != nil {
		t.Fatalf("ftruncate unlinked: %v", err)
	}
	if err := gone.Chmod(0o600); err != nil {
		t.Fatalf("fchmod unlinked: %v", err)
	}
	st, err := gone.Stat()
	if err != nil || st.Size() != 4 || st.Mode().Perm() != 0o600 {
		t.Fatalf("unlinked object: %v err=%v", st, err)
	}

	old, err := os.OpenFile(filepath.Join(v.mnt, "old"), os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open old: %v", err)
	}
	defer func() { _ = old.Close() }()
	if err := os.Rename(filepath.Join(v.mnt, "new"), filepath.Join(v.mnt, "old")); err != nil {
		t.Fatalf("rename over: %v", err)
	}
	if err := old.Truncate(3); err != nil {
		t.Fatalf("ftruncate replaced: %v", err)
	}
	if err := old.Chmod(0o640); err != nil {
		t.Fatalf("fchmod replaced: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(v.mnt, "old")); err != nil || !bytes.Equal(got, []byte("new")) {
		t.Fatalf("replacement touched: %q err=%v", got, err)
	}
	if info, err := os.Stat(filepath.Join(v.mnt, "old")); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("replacement mode: %v err=%v", info, err)
	}
}
