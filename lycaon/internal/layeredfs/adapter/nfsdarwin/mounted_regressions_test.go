//go:build darwin

package nfsdarwin_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/fssync"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/nfsdarwin"
)

// settleMargin leaves room for a failing test to unmount a view whose server may be slow to answer.
const settleMargin = 30 * time.Second

type mountedView struct {
	adp                 *nfsdarwin.Adapter
	id, mnt, trunk, tmp string
	runtimeDir          string
	core                *layeredfs.Core
}

func prepareRealMount(t *testing.T, cfg nfsdarwin.Config, seed map[string]string) *mountedView {
	t.Helper()
	tmp := t.TempDir()
	v := &mountedView{adp: nfsdarwin.New(cfg), id: "view-regress", tmp: tmp, trunk: filepath.Join(tmp, "trunk"), runtimeDir: filepath.Join(tmp, "runtime")}
	if err := os.MkdirAll(v.trunk, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	for name, content := range seed {
		p := filepath.Join(v.trunk, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir seed: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	prep, err := v.adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: v.id, BackingDir: filepath.Join(tmp, "backing"), RuntimeDir: v.runtimeDir,
		QuiesceHook: nfsdarwin.NoopQuiesceHook, FlushClientHook: nfsdarwin.NoWritableMappingsFlushHook,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: v.trunk, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	v.mnt, v.core = prep.PrimaryWorkPath, prep.Core
	releaseWhenDone(t, v.adp, v.id)
	return v
}

// settlesBeforeTestDeadline returns a context that ends while the test can still fail and run its
// cleanups. A package deadline panics the binary, which leaves every mount it holds behind.
func settlesBeforeTestDeadline(t *testing.T) context.Context {
	t.Helper()
	deadline, ok := t.Deadline()
	if !ok {
		return t.Context()
	}
	ctx, cancel := context.WithDeadline(t.Context(), deadline.Add(-settleMargin))
	t.Cleanup(cancel)
	return ctx
}

// releaseWhenDone settles a view however its test ends. t.Context is already canceled in a cleanup,
// and a failed release must not leave the mount for the next run to find.
func releaseWhenDone(t *testing.T, adp *nfsdarwin.Adapter, viewID string) {
	t.Helper()
	facts, _ := adp.MountFactsForView(viewID)
	t.Cleanup(func() {
		rcpt, err := adp.Release(context.Background(), viewID)
		if err != nil && rcpt.ReasonCode != adapter.ReasonViewNotFound {
			t.Errorf("release left the view behind: %+v err=%v", rcpt, err)
			_ = exec.Command(nfsdarwin.DefaultUmountBin, "-f", facts.MountPath).Run()
		}
	})
}

func (v *mountedView) shell(t *testing.T, script string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", script)
	cmd.Dir = v.mnt
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shell %q: %v (%s)", script, err, out)
	}
	return string(out)
}

func (v *mountedView) barrier(t *testing.T) {
	t.Helper()
	_, release, err := v.adp.CheckpointBarrier(t.Context(), v.id)
	if err != nil {
		t.Fatalf("barrier: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("barrier release: %v", err)
	}
}

// TestReadOnlyCreateAndLaterChmodKeepAcknowledgedBytes uses the real kernel client, whose
// open(O_CREAT|O_EXCL, 0444) is an EXCLUSIVE create plus SETATTR. Git loose objects take this path.
// A write here returns success from the client cache, so a refusing server loses the bytes silently.
func TestReadOnlyCreateAndLaterChmodKeepAcknowledgedBytes(t *testing.T) {
	v := prepareRealMount(t, nfsdarwin.Config{}, nil)
	p := filepath.Join(v.mnt, "tmp_obj")
	fd, err := syscall.Open(p, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL, 0o444)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := syscall.Write(fd, []byte("object bytes")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := syscall.Fsync(fd); err != nil {
		t.Fatalf("fsync of 0444 create: %v", err)
	}
	if err := syscall.Close(fd); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got, err := os.ReadFile(p); err != nil || string(got) != "object bytes" {
		t.Fatalf("object read back: %q err=%v", got, err)
	}
	if _, err := os.OpenFile(p, os.O_WRONLY, 0); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("reopening 0444 for write must fail like a native file: %v", err)
	}

	q := filepath.Join(v.mnt, "chmod_after")
	f, err := os.OpenFile(q, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.Chmod(0o400); err != nil {
		t.Fatalf("fchmod: %v", err)
	}
	if _, err := f.Write([]byte("after chmod")); err != nil {
		t.Fatalf("write after fchmod: %v", err)
	}
	if err := fssync.Always(f); err != nil {
		t.Fatalf("fsync after fchmod: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got, err := os.ReadFile(q); err != nil || string(got) != "after chmod" {
		t.Fatalf("read back after fchmod: %q err=%v", got, err)
	}
}

// TestHeldDescriptorAndWorkingDirectorySurviveLeavingTheHandleTable walks more files than the table
// holds. A descriptor or working directory the client never revisited must still work.
func TestHeldDescriptorAndWorkingDirectorySurviveLeavingTheHandleTable(t *testing.T) {
	seed := map[string]string{"held.txt": "held content", "cwd/inner.txt": "x"}
	for i := range 200 {
		seed[fmt.Sprintf("many/f%03d", i)] = "x"
	}
	v := prepareRealMount(t, nfsdarwin.Config{MaxHandles: 32}, seed)
	f, err := os.Open(filepath.Join(v.mnt, "held.txt"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	holder := exec.Command("/bin/sh", "-c", `cd cwd && read line && ls inner.txt && pwd`)
	holder.Dir = v.mnt
	stdin, err := holder.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	var out strings.Builder
	holder.Stdout, holder.Stderr = &out, &out
	if err := holder.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	v.shell(t, `sleep 0; for f in many/*; do cat "$f" > /dev/null; done; ls -l many > /dev/null`)
	if got := v.adp.ServerForView(v.id).HandleCount(); got > 32+8 {
		t.Fatalf("handle table holds %d records, limit is 32 plus directories", got)
	}
	if _, err := f.Stat(); err != nil {
		t.Fatalf("fstat after walk: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Fatalf("read after walk: %v", err)
	}
	_, _ = io.WriteString(stdin, "go\n")
	_ = stdin.Close()
	if err := holder.Wait(); err != nil {
		t.Fatalf("working directory after walk: %v (%s)", err, out.String())
	}
	if !strings.Contains(out.String(), "inner.txt") {
		t.Fatalf("working directory after walk: %s", out.String())
	}
}

func TestBarrierCostFollowsChangesNotReads(t *testing.T) {
	seed := map[string]string{}
	for i := range 50 {
		seed[fmt.Sprintf("many/f%03d", i)] = "x"
	}
	v := prepareRealMount(t, nfsdarwin.Config{}, seed)
	v.shell(t, `find . -type f | wc -l; cat many/* > /dev/null`)
	v.barrier(t)
	if files, _ := v.adp.LastFlushStatsForView(v.id); files != 0 {
		t.Fatalf("reading files made %d of them flush candidates", files)
	}
	v.shell(t, `echo changed > many/f000`)
	v.barrier(t)
	if files, _ := v.adp.LastFlushStatsForView(v.id); files == 0 || files > 2 {
		t.Fatalf("one changed file flushed %d files", files)
	}
}

func TestUnreadableChangedFileDoesNotWedgeTheBarrierAndIsCaptured(t *testing.T) {
	v := prepareRealMount(t, nfsdarwin.Config{}, nil)
	p := filepath.Join(v.mnt, "secret")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.WriteString("buffered"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(p, 0); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	v.barrier(t)
	if got, err := v.core.ReadFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "secret"}); err != nil || string(got) != "buffered" {
		t.Fatalf("buffered bytes behind a mode 000 file: %q err=%v", got, err)
	}
	_ = f.Close()
}

// TestBarrierCapturesBufferedWritesOfADescriptorStillOpen: the writer has neither closed nor
// fsynced, so the server has seen nothing. The barrier has to reach the client's buffers.
func TestBarrierCapturesBufferedWritesOfADescriptorStillOpen(t *testing.T) {
	v := prepareRealMount(t, nfsdarwin.Config{}, nil)
	p := filepath.Join(v.mnt, "buffered")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = f.Close() }()
	want := strings.Repeat("buffered-data-", 1000)
	if _, err := f.WriteString(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	v.barrier(t)
	if got, err := v.core.ReadFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "buffered"}); err != nil || string(got) != want {
		t.Fatalf("buffered bytes in Core: %d of %d, err=%v", len(got), len(want), err)
	}
}

func TestSpentCapabilityRefusesASecondMountFromTheMountTable(t *testing.T) {
	v := prepareRealMount(t, nfsdarwin.Config{}, map[string]string{"f.txt": "x"})
	facts, ok := v.adp.MountFactsForView(v.id)
	if !ok {
		t.Fatal("missing facts")
	}
	dup := filepath.Join(v.tmp, "dup")
	if err := os.MkdirAll(dup, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	out, err := exec.Command(nfsdarwin.DefaultMountNFSBin, "-o", nfsdarwin.FormatMountOptions(facts.ServerPort), facts.ExportSource, dup).CombinedOutput()
	if err == nil {
		_ = exec.Command(nfsdarwin.DefaultUmountBin, "-f", dup).Run()
		t.Fatalf("a second client mounted the export from the mount table: %s", out)
	}
	if got, err := os.ReadFile(filepath.Join(v.mnt, "f.txt")); err != nil || string(got) != "x" {
		t.Fatalf("live mount after refused second mount: %q err=%v", got, err)
	}
}

func TestPosixAndFlockLocksWorkBetweenProcesses(t *testing.T) {
	v := prepareRealMount(t, nfsdarwin.Config{}, nil)
	if !v.adp.Facts().MetadataCapabilities.SupportsPOSIXLocks {
		t.Fatal("adapter must declare POSIX locks")
	}
	p := filepath.Join(v.mnt, "lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = f.Close() }()
	lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 0, Len: 10}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock); err != nil {
		t.Fatalf("F_SETLK: %v", err)
	}
	// Locks conflict per process, so the contender is this test binary run again.
	contender := func(start string) error {
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), "NFSDARWIN_LOCK_PROBE="+p, "NFSDARWIN_LOCK_START="+start)
		return cmd.Run()
	}
	var exit *exec.ExitError
	if err := contender("5"); !errors.As(err, &exit) || exit.ExitCode() != lockProbeBusy {
		t.Fatalf("overlapping lock from another process: %v", err)
	}
	if err := contender("20"); err != nil {
		t.Fatalf("disjoint lock from another process: %v", err)
	}

	q, err := os.OpenFile(filepath.Join(v.mnt, "flock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = q.Close() }()
	g, err := os.Open(q.Name())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = g.Close() }()
	if err := syscall.Flock(int(q.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock: %v", err)
	}
	if err := syscall.Flock(int(g.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("conflicting flock: %v", err)
	}
}

const lockProbeBusy = 7

// runLockProbe is the second process of TestPosixAndFlockLocksWorkBetweenProcesses.
func runLockProbe(path, start string) int {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return 1
	}
	defer func() { _ = f.Close() }()
	offset, err := strconv.ParseInt(start, 10, 64)
	if err != nil {
		return 1
	}
	lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: offset, Len: 1}
	switch err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock); {
	case err == nil:
		return 0
	case errors.Is(err, syscall.EAGAIN), errors.Is(err, syscall.EACCES):
		return lockProbeBusy
	default:
		return 1
	}
}

func TestFIFOAndUnixSocketWorkOnTheMount(t *testing.T) {
	v := prepareRealMount(t, nfsdarwin.Config{}, nil)
	if err := syscall.Mkfifo(filepath.Join(v.mnt, "pipe"), 0o640); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	info, err := os.Lstat(filepath.Join(v.mnt, "pipe"))
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 || info.Mode().Perm() != 0o640 {
		t.Fatalf("fifo attributes: %v err=%v", info, err)
	}
	// The macOS NFS client refuses open(2) on any FIFO whatever the server or mount options say. The
	// node still exists, lists and removes correctly, so a server regression shows here as ENOENT.
	if fd, err := syscall.Open(filepath.Join(v.mnt, "pipe"), syscall.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
		_ = syscall.Close(fd)
	} else if !errors.Is(err, syscall.EACCES) {
		t.Fatalf("fifo open: %v", err)
	}
	sock := filepath.Join(shortPathTo(t, v.mnt), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("bind on mount: %v", err)
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
		t.Fatalf("connect on mount: %v", err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "hi" {
		t.Fatalf("socket carried %q err=%v", buf, err)
	}
	_ = conn.Close()
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
	for _, ch := range v.publishedChanges(t) {
		if ch.Path.Rel == "pipe" || ch.Path.Rel == "s.sock" {
			t.Fatalf("special node in the change set: %+v", ch)
		}
	}
	v.barrier(t)
	if err := os.Remove(filepath.Join(v.mnt, "pipe")); err != nil {
		t.Fatalf("remove fifo: %v", err)
	}
}

func (v *mountedView) publishedChanges(t *testing.T) []layeredfs.ChangeEntry {
	t.Helper()
	changes, err := v.core.EnumerateChanges(true)
	if err != nil {
		t.Fatalf("changes: %v", err)
	}
	return changes
}

// shortPathTo returns a path to dir short enough for a Unix socket address.
func shortPathTo(t *testing.T, dir string) string {
	t.Helper()
	parent, err := os.MkdirTemp("/tmp", "nf")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	link := filepath.Join(parent, "m")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	return link
}
