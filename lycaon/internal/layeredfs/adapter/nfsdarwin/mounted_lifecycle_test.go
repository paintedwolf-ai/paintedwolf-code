package nfsdarwin_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"testing"
	"unsafe"

	"github.com/lycaon/lycaon/internal/fssync"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/nfsdarwin"
	"github.com/lycaon/lycaon/internal/layeredfs/nfsserver"
)

// TestOpenThenDeletedFileNeverReachesTheChangeSet: the client renames an open file to a placeholder
// instead of removing it. FUSE has no such name, and both adapters must publish the same result.
func TestOpenThenDeletedFileNeverReachesTheChangeSet(t *testing.T) {
	v := prepareRealMount(t, nfsdarwin.Config{}, map[string]string{"lower.txt": "lower"})
	p := filepath.Join(v.mnt, "open-unlink")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString("data"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := fssync.Always(f); err != nil {
		t.Fatalf("fsync: %v", err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := f.Truncate(2); err != nil {
		t.Fatalf("ftruncate unlinked: %v", err)
	}
	if err := f.Chmod(0o600); err != nil {
		t.Fatalf("fchmod unlinked: %v", err)
	}
	if st, err := f.Stat(); err != nil || st.Size() != 2 || st.Mode().Perm() != 0o600 {
		t.Fatalf("fstat unlinked: %v err=%v", st, err)
	}
	if changes := v.publishedChanges(t); len(changes) != 0 {
		t.Fatalf("placeholder reached the change set: %+v", changes)
	}
}

func TestReleaseKeepsAcknowledgedUnstableBytes(t *testing.T) {
	v := prepareRealMount(t, nfsdarwin.Config{}, map[string]string{"f": "x"})
	srv := v.adp.ServerForView(v.id)
	conn, root := mountTestNFSConn(t, srv.Addr().String(), extraTestExport(t, srv, v.id), uint32(os.Getuid()), uint32(os.Getgid()))
	defer func() { _ = conn.Close() }()
	fh := lookupTestNFSFile(t, conn, root, "f", uint32(os.Getuid()), uint32(os.Getgid()))
	if st := sendTestNFSWrite(t, conn, fh, 0, []byte("held only by the server"), uint32(nfsserver.Unstable), uint32(os.Getuid()), uint32(os.Getgid())); st != 0 {
		t.Fatalf("write: %v", st)
	}
	backing := filepath.Join(v.tmp, "backing")
	if _, err := v.adp.Release(t.Context(), v.id); err != nil {
		t.Fatalf("release: %v", err)
	}
	found := false
	entries, err := os.ReadDir(filepath.Join(backing, "working"))
	if err != nil {
		t.Fatalf("read working: %v", err)
	}
	for _, e := range entries {
		if got, rErr := os.ReadFile(filepath.Join(backing, "working", e.Name())); rErr == nil && string(got) == "held only by the server" {
			found = true
		}
	}
	if !found {
		t.Fatal("acknowledged bytes were dropped at release")
	}
}

func TestMultipleRootsMountSeparatelyAndBarrierCoversBoth(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("nfsdarwin requires darwin")
	}
	tmp := t.TempDir()
	a, b := filepath.Join(tmp, "a"), filepath.Join(tmp, "b")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(a, "shared-name"), []byte("in-a"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(b, "shared-name"), []byte("in-b"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	adp := nfsdarwin.New(nfsdarwin.Config{})
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: "view-multi", BackingDir: filepath.Join(tmp, "backing"), RuntimeDir: filepath.Join(tmp, "runtime"),
		QuiesceHook: nfsdarwin.NoopQuiesceHook, FlushClientHook: nfsdarwin.NoWritableMappingsFlushHook,
		Grants: []adapter.RootGrant{
			{RootID: "first", Label: "first", SourcePath: a, IsPrimary: true},
			{RootID: "second", Label: "second", SourcePath: b},
		},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(context.Background(), "view-multi") })
	if len(prep.Roots) != 2 {
		t.Fatalf("roots: %+v", prep.Roots)
	}
	paths := map[string]string{}
	for _, r := range prep.Roots {
		paths[r.RootID] = r.WorkspacePath
	}
	for root, want := range map[string]string{"first": "in-a", "second": "in-b"} {
		if got, err := os.ReadFile(filepath.Join(paths[root], "shared-name")); err != nil || string(got) != want {
			t.Fatalf("root %s reads %q err=%v", root, got, err)
		}
		f, err := os.OpenFile(filepath.Join(paths[root], "dirty"), os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("create in %s: %v", root, err)
		}
		defer func() { _ = f.Close() }()
		if _, err := f.WriteString("dirty-" + root); err != nil {
			t.Fatalf("write in %s: %v", root, err)
		}
	}
	_, release, err := adp.CheckpointBarrier(t.Context(), "view-multi")
	if err != nil {
		t.Fatalf("barrier: %v", err)
	}
	for _, root := range []string{"first", "second"} {
		got, err := prep.Core.ReadFile(layeredfs.QualifiedPath{RootID: root, Rel: "dirty"})
		if err != nil || string(got) != "dirty-"+root {
			t.Fatalf("root %s captured %q err=%v", root, got, err)
		}
	}
	if err := release(); err != nil {
		t.Fatalf("release barrier: %v", err)
	}
	if files, _ := adp.LastFlushStatsForView("view-multi"); files < 2 {
		t.Fatalf("flush stats cover %d files across two roots", files)
	}
	if _, err := adp.Release(t.Context(), "view-multi"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	for root, p := range paths {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("root %s mount path remains", root)
		}
	}
}

func TestReleaseNeverDeletesThroughAMountItDoesNotRecognize(t *testing.T) {
	v := prepareRealMount(t, nfsdarwin.Config{}, map[string]string{"survivor.txt": "x"})
	other := prepareRealMount(t, nfsdarwin.Config{}, map[string]string{"foreign.txt": "keep"})
	nested := filepath.Join(v.runtimeDir, v.id, "foreign")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	srv := other.adp.ServerForView(other.id)
	facts, _ := other.adp.MountFactsForView(other.id)
	exportPath := extraTestExport(t, srv, other.id)
	out, err := exec.Command(nfsdarwin.DefaultMountNFSBin, "-o", nfsdarwin.FormatMountOptions(facts.ServerPort), "127.0.0.1:"+exportPath, nested).CombinedOutput()
	if err != nil {
		t.Fatalf("foreign mount: %v (%s)", err, out)
	}
	t.Cleanup(func() { _ = exec.Command(nfsdarwin.DefaultUmountBin, "-f", nested).Run() })
	if _, err := v.adp.Release(t.Context(), v.id); err == nil {
		t.Fatal("release removed a runtime dir with a live foreign mount below it")
	}
	if got, err := os.ReadFile(filepath.Join(nested, "foreign.txt")); err != nil || string(got) != "keep" {
		t.Fatalf("foreign mount damaged: %q err=%v", got, err)
	}
	if out, err := exec.Command(nfsdarwin.DefaultUmountBin, "-f", nested).CombinedOutput(); err != nil {
		t.Fatalf("umount foreign: %v (%s)", err, out)
	}
	if rcpt, err := v.adp.Release(t.Context(), v.id); err != nil || !rcpt.Settled {
		t.Fatalf("release retry: %+v err=%v", rcpt, err)
	}
}

func TestReclaimDeadMountsTouchesOnlyUnservedViewMounts(t *testing.T) {
	v := prepareRealMount(t, nfsdarwin.Config{}, map[string]string{"f": "x"})
	// A fresh adapter models a restarted engine: the mount belongs to a server it does not run.
	restarted := nfsdarwin.New(nfsdarwin.Config{})
	if got, err := v.adp.ReclaimDeadMounts(t.Context(), v.runtimeDir); err != nil || len(got) != 0 {
		t.Fatalf("a served mount was reclaimed: %v err=%v", got, err)
	}
	got, err := restarted.ReclaimDeadMounts(t.Context(), filepath.Join(v.tmp, "elsewhere"))
	if err != nil || len(got) != 0 {
		t.Fatalf("reclaim outside the runtime root: %v err=%v", got, err)
	}
	got, err = restarted.ReclaimDeadMounts(t.Context(), v.runtimeDir)
	if err != nil || len(got) != 1 {
		t.Fatalf("reclaim: %v err=%v", got, err)
	}
	if v.adp.IsViewActive(v.id) {
		t.Fatal("view still active after its mount was reclaimed")
	}
}

// TestEngineStartsProcessesInItsOwnMountUnderStopTheWorld is the macOS half of the Linux F1 hang.
// Go forks here rather than vforks, so a child's chdir into the mount does not hold a scheduler
// slot. A regression shows as a hang caught by the test deadline, not as a slow pass.
func TestEngineStartsProcessesInItsOwnMountUnderStopTheWorld(t *testing.T) {
	v := prepareRealMount(t, nfsdarwin.Config{}, map[string]string{"sub/deep/f": "x"})
	if err := os.WriteFile(filepath.Join(v.mnt, "tool.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write tool: %v", err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				runtime.GC()
			}
		}
	}()
	defer wg.Wait()
	defer close(stop)
	sub := filepath.Join(v.mnt, "sub/deep")
	for i := range 100 {
		dirCmd := exec.Command("/usr/bin/true")
		dirCmd.Dir = sub
		if err := dirCmd.Run(); err != nil {
			t.Fatalf("iteration %d working directory in mount: %v", i, err)
		}
		if err := exec.Command(filepath.Join(v.mnt, "tool.sh")).Run(); err != nil {
			t.Fatalf("iteration %d program from mount: %v", i, err)
		}
		f, err := os.Open(filepath.Join(sub, "f"))
		if err != nil {
			t.Fatalf("iteration %d open: %v", i, err)
		}
		_ = f.Close()
	}
}

// TestXattrsRoundTripThroughTheKernelSidecar drives the system's xattr tool. A file with attributes
// in the trunk is presented through a synthesized ._ file the kernel must be able to parse.
func TestXattrsRoundTripThroughTheKernelSidecar(t *testing.T) {
	v := prepareRealMount(t, nfsdarwin.Config{}, map[string]string{"lower.txt": "x", "plain.txt": "y"})
	lowerTrunk := filepath.Join(v.trunk, "lower.txt")
	if out, err := exec.Command("/usr/bin/xattr", "-w", "user.lower", "from-trunk", lowerTrunk).CombinedOutput(); err != nil {
		t.Fatalf("seed xattr: %v (%s)", err, out)
	}
	if err := os.WriteFile(filepath.Join(v.trunk, "probe-listing"), nil, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := v.shell(t, `xattr -p user.lower lower.txt`); strings.TrimSpace(got) != "from-trunk" {
		t.Fatalf("trunk attribute through the mount: %q", got)
	}
	v.shell(t, `xattr -w user.added two lower.txt`)
	if got := v.shell(t, `xattr -p user.added lower.txt; xattr -p user.lower lower.txt`); got != "two\nfrom-trunk\n" {
		t.Fatalf("after adding: %q", got)
	}
	v.shell(t, `xattr -d user.lower lower.txt`)
	if got := v.shell(t, `xattr lower.txt`); strings.Contains(got, "user.lower") || !strings.Contains(got, "user.added") {
		t.Fatalf("after deleting: %q", got)
	}
	attr, err := v.core.Lstat(layeredfs.QualifiedPath{RootID: "primary", Rel: "lower.txt"})
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if _, kept := attr.Xattrs["user.lower"]; kept || string(attr.Xattrs["user.added"]) != "two" {
		t.Fatalf("core attributes: %v", attr.Xattrs)
	}
	v.shell(t, `xattr -w user.plain v plain.txt; xattr -c plain.txt`)
	if got := v.shell(t, `xattr plain.txt`); strings.Contains(got, "user.plain") {
		t.Fatalf("after clearing: %q", got)
	}
	if out := v.shell(t, `cp -p lower.txt copy.txt && xattr -p user.added copy.txt`); strings.TrimSpace(out) != "two" {
		t.Fatalf("cp -p kept: %q", out)
	}
	if trunkVal, err := exec.Command("/usr/bin/xattr", "-p", "user.lower", lowerTrunk).CombinedOutput(); err != nil || strings.TrimSpace(string(trunkVal)) != "from-trunk" {
		t.Fatalf("trunk changed: %q err=%v", trunkVal, err)
	}
}

// TestArchiveAndCopyToolsExitCleanly holds the card's named tools to their native exit codes.
func TestArchiveAndCopyToolsExitCleanly(t *testing.T) {
	v := prepareRealMount(t, nfsdarwin.Config{}, nil)
	script := `set -e
mkdir src
echo content > src/a
echo script > src/x.sh
chmod 755 src/x.sh
chmod 444 src/a
ln -s a src/link
ln src/x.sh src/hard
touch -t 200001020304 src/a
tar -cf t.tar src
mkdir tx
tar -xpf t.tar -C tx
cp -pR src cpr
rsync -a src/ rs/
install -m 0644 src/x.sh inst
stat -f '%Sp %Sm %l' -t %Y tx/src/a cpr/a rs/a tx/src/x.sh inst`
	got := strings.Fields(v.shell(t, script))
	want := []string{
		"-r--r--r--", "2000", "1", "-r--r--r--", "2000", "1", "-r--r--r--", "2000", "1",
		"-rwxr-xr-x", "", "2", "-rw-r--r--", "", "1",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, field := range want {
		if field != "" && got[i] != field {
			t.Fatalf("metadata field %d: got %v, want %v", i, got, want)
		}
	}
}

func TestAtomicTrunkReplacementIsSeenAtOnce(t *testing.T) {
	v := prepareRealMount(t, nfsdarwin.Config{}, map[string]string{"f.txt": "old\n"})
	if got := v.shell(t, `cat f.txt`); got != "old\n" {
		t.Fatalf("before: %q", got)
	}
	tmp := filepath.Join(v.trunk, "tmp")
	if err := os.WriteFile(tmp, []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Rename(tmp, filepath.Join(v.trunk, "f.txt")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got := v.shell(t, `cat f.txt`); got != "new\n" {
		t.Fatalf("after: %q", got)
	}
}

// TestBarrierCapturesPagesAnotherProcessHoldsDirtyInAMapping: the writer is alive with its mapping,
// has not called msync, and its dirty pages are invisible to fsync and to a volume sync.
func TestBarrierCapturesPagesAnotherProcessHoldsDirtyInAMapping(t *testing.T) {
	v := prepareRealMount(t, nfsdarwin.Config{}, nil)
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "NFSDARWIN_MMAP_PROBE="+filepath.Join(v.mnt, "mapped.bin"))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()
	ready := make([]byte, 1)
	if _, err := stdout.Read(ready); err != nil {
		t.Fatalf("mapped writer: %v", err)
	}
	v.barrier(t)
	got, err := v.core.ReadFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "mapped.bin"})
	if err != nil || len(got) < 5 || string(got[:5]) != "DIRTY" {
		t.Fatalf("mapped bytes after barrier: %q err=%v", got, err)
	}
}

func runMappedWriter(path string) int {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return 1
	}
	if err := f.Truncate(65536); err != nil {
		return 1
	}
	mapped, err := syscall.Mmap(int(f.Fd()), 0, 65536, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return 1
	}
	copy(mapped, "DIRTY")
	_, _ = os.Stdout.Write([]byte("r"))
	_, _ = io.ReadAll(os.Stdin)
	return 0
}

// copyToMapping writes through an in-process mapping of a file on the mount. The page fault holds
// the scheduler slot until the server replies, so a test that must do this gives the server a slot
// and keeps a stop-the-world from waiting on the fault. The engine itself never maps mount files.
func copyToMapping(dst, src []byte) {
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(max(runtime.GOMAXPROCS(0), 2)))
	copy(dst, src)
}

func msyncForTest(mapped []byte) error {
	_, _, errno := syscall.Syscall(syscall.SYS_MSYNC, uintptr(unsafe.Pointer(&mapped[0])), uintptr(len(mapped)), uintptr(syscall.MS_SYNC))
	if errno != 0 {
		return errno
	}
	return nil
}

// TestOneSchedulerSlotServesAMountedWorkflow runs everything the engine does against its own mount
// in a child limited to one slot, where anything holding the slot across a server reply deadlocks.
func TestOneSchedulerSlotServesAMountedWorkflow(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("nfsdarwin requires darwin")
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0])
	cmd.Env = append(os.Environ(), "NFSDARWIN_ONE_SLOT_PROBE="+t.TempDir(), "GOMAXPROCS=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("one-slot workflow: %v\n%s", err, out)
	}
}

func runOneSlotWorkflow() int {
	fail := func(step string, err error) int {
		fmt.Fprintf(os.Stderr, "%s: %v\n", step, err)
		return 1
	}
	if runtime.GOMAXPROCS(0) != 1 {
		return fail("slots", fmt.Errorf("GOMAXPROCS=%d", runtime.GOMAXPROCS(0)))
	}
	tmp := os.Getenv("NFSDARWIN_ONE_SLOT_PROBE")
	trunk := filepath.Join(tmp, "trunk")
	if err := os.MkdirAll(filepath.Join(trunk, "sub"), 0o755); err != nil {
		return fail("seed", err)
	}
	if err := os.WriteFile(filepath.Join(trunk, "sub", "f"), []byte("x"), 0o644); err != nil {
		return fail("seed", err)
	}
	ctx := context.Background()
	adp := nfsdarwin.New(nfsdarwin.Config{})
	prep, err := adp.Prepare(ctx, adapter.PrepareRequest{
		ViewID: "view-one-slot", BackingDir: filepath.Join(tmp, "backing"), RuntimeDir: filepath.Join(tmp, "runtime"),
		QuiesceHook: nfsdarwin.NoopQuiesceHook, FlushClientHook: nfsdarwin.NoWritableMappingsFlushHook,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunk, IsPrimary: true}},
	})
	if err != nil {
		return fail("prepare", err)
	}
	sub := filepath.Join(prep.PrimaryWorkPath, "sub")
	f, err := os.OpenFile(filepath.Join(sub, "written"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fail("create", err)
	}
	if _, err := f.WriteString("data"); err != nil {
		return fail("write", err)
	}
	if err := fssync.Always(f); err != nil {
		return fail("fsync", err)
	}
	if err := f.Close(); err != nil {
		return fail("close", err)
	}
	if got, err := os.ReadFile(filepath.Join(sub, "f")); err != nil || string(got) != "x" {
		return fail("read", err)
	}
	if _, err := os.ReadDir(sub); err != nil {
		return fail("readdir", err)
	}
	child := exec.Command("/bin/sh", "-c", "cat written")
	child.Dir = sub
	if out, err := child.CombinedOutput(); err != nil || string(out) != "data" {
		return fail("child in mount", err)
	}
	_, release, err := adp.CheckpointBarrier(ctx, "view-one-slot")
	if err != nil {
		return fail("barrier", err)
	}
	if err := release(); err != nil {
		return fail("barrier release", err)
	}
	if _, err := adp.Release(ctx, "view-one-slot"); err != nil {
		return fail("release", err)
	}
	return 0
}
