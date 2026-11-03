package fuselinux_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/fuselinux"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"golang.org/x/sys/unix"
)

type mountedView struct {
	adp                         *fuselinux.Adapter
	viewID, mnt, trunk, backing string
	runtimeDir                  string
	core                        *layeredfs.Core
}

func prepareMountedView(t *testing.T, cfg fuselinux.Config, viewID string, seed map[string]string) *mountedView {
	t.Helper()
	adp := fuselinux.New(cfg)
	tmp := t.TempDir()
	v := &mountedView{adp: adp, viewID: viewID, trunk: filepath.Join(tmp, "trunk"), backing: filepath.Join(tmp, "backing"), runtimeDir: filepath.Join(tmp, "runtime")}
	for _, d := range []string{v.trunk, v.backing, v.runtimeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	for name, content := range seed {
		if err := os.WriteFile(filepath.Join(v.trunk, name), []byte(content), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: viewID, BackingDir: v.backing, RuntimeDir: v.runtimeDir,
		QuiesceHook: fuselinux.NoopQuiesceHook, FlushClientHook: fuselinux.NoWritableMappingsFlushHook,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: v.trunk, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	v.mnt, v.core = prep.PrimaryWorkPath, prep.Core
	t.Cleanup(func() {
		adp.SetFusermountBinForTest("")
		adp.SetConfigCallbacksForTest(nil, nil)
		_, _ = adp.Release(context.Background(), viewID)
	})
	return v
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

// TestNativeShellCoherenceMatrix crosses each mutation with each writer and reader: a change made
// one way is visible the other way at once, with no retry, for every kind of change.
func TestNativeShellCoherenceMatrix(t *testing.T) {
	v := prepareMountedView(t, fuselinux.Config{}, "view-coherence-matrix", nil)
	p := func(name string) string { return filepath.Join(v.mnt, name) }
	native := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("native mutation: %v", err)
		}
	}
	nativeRead := func(name string) string {
		raw, err := os.ReadFile(p(name))
		if err != nil {
			return "<" + err.Error() + ">"
		}
		return string(raw)
	}
	shellRead := func(name string) string {
		return v.shell(t, "if [ -e "+name+" ]; then cat "+name+"; else echo -n '<absent>'; fi")
	}
	missing := func(read func(string) string, name string) bool {
		got := read(name)
		return got == "<absent>" || strings.Contains(got, "no such file")
	}

	for _, dir := range []struct {
		name string
		mut  func(name, op, arg string)
		read func(string) string
	}{
		{"native write then shell read", func(name, op, arg string) {
			switch op {
			case "create", "edit":
				native(os.WriteFile(p(name), []byte(arg), 0o644))
			case "truncate":
				native(os.Truncate(p(name), 0))
			case "rename":
				native(os.Rename(p(name), p(arg)))
			case "delete":
				native(os.Remove(p(name)))
			}
		}, shellRead},
		{"shell write then native read", func(name, op, arg string) {
			switch op {
			case "create", "edit":
				v.shell(t, "printf %s "+arg+" > "+name)
			case "truncate":
				v.shell(t, ": > "+name)
			case "rename":
				v.shell(t, "mv "+name+" "+arg)
			case "delete":
				v.shell(t, "rm "+name)
			}
		}, nativeRead},
	} {
		t.Run(dir.name, func(t *testing.T) {
			const a, b = "subject", "renamed"
			dir.mut(a, "create", "AAAA")
			if got := dir.read(a); got != "AAAA" {
				t.Fatalf("after create: %q", got)
			}
			dir.mut(a, "edit", "BBBB")
			if got := dir.read(a); got != "BBBB" {
				t.Fatalf("after same-size edit: %q", got)
			}
			dir.mut(a, "truncate", "")
			if got := dir.read(a); got != "" {
				t.Fatalf("after truncate: %q", got)
			}
			dir.mut(a, "edit", "CCCC")
			dir.mut(a, "rename", b)
			if got := dir.read(b); got != "CCCC" || !missing(dir.read, a) {
				t.Fatalf("after rename: new=%q old=%q", got, dir.read(a))
			}
			dir.mut(b, "delete", "")
			if !missing(dir.read, b) {
				t.Fatalf("after delete: %q", dir.read(b))
			}
		})
	}
	if entries, err := os.ReadDir(v.trunk); err != nil || len(entries) != 0 {
		t.Fatalf("worker mutations reached trunk: %v err=%v", entries, err)
	}
}

func TestMountFactsRecordEnforcedOptionsAndNegotiatedCapabilities(t *testing.T) {
	v := prepareMountedView(t, fuselinux.Config{}, "view-facts", nil)
	mf, ok := v.adp.MountFactsForView(v.viewID)
	if !ok {
		t.Fatal("no mount facts")
	}
	for _, want := range []string{"nosuid", "nodev"} {
		if !strings.Contains(mf.MountOptions, want) {
			t.Errorf("mount options %q missing %s", mf.MountOptions, want)
		}
	}
	if !strings.Contains(mf.SuperOptions, "default_permissions") || strings.Contains(mf.SuperOptions, "allow_other") {
		t.Errorf("unexpected super options %q", mf.SuperOptions)
	}
	if mf.KernelMinor < fuselinux.FUSEProtocolMinor || mf.GrantedFlags == 0 {
		t.Errorf("negotiation not recorded: %+v", mf)
	}
	const writebackCache, parallelDirops = 1 << 16, 1 << 18
	if mf.GrantedFlags&(writebackCache|parallelDirops) != 0 {
		t.Errorf("granted flags %#x include a cache mode the adapter does not define", mf.GrantedFlags)
	}
}

// TestOpensInThisProcessNeverWaitOnTheServerToRegisterWithThePoller pins the opt-out: the only
// FUSE_POLL is the one spent during Prepare, and the probe entry is gone afterwards.
func TestOpensInThisProcessNeverWaitOnTheServerToRegisterWithThePoller(t *testing.T) {
	v := prepareMountedView(t, fuselinux.Config{}, "view-poll", map[string]string{"f": "x"})
	srv := v.adp.ServerForView(v.viewID)
	for range 3 {
		f, err := os.Open(filepath.Join(v.mnt, "f"))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		_ = f.Close()
	}
	if got := srv.PollRequestsForTest(); got != 1 {
		t.Fatalf("FUSE_POLL requests = %d, want exactly the one from Prepare", got)
	}
	entries, err := os.ReadDir(v.mnt)
	if err != nil || len(entries) != 1 || entries[0].Name() != "f" {
		t.Fatalf("mount listing %v err=%v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(v.mnt, ".painted-wolf-poll-probe")); !os.IsNotExist(err) {
		t.Fatalf("probe entry visible after Prepare: %v", err)
	}
}

func TestWorkerFileModesAreEnforcedOnTheMount(t *testing.T) {
	v := prepareMountedView(t, fuselinux.Config{}, "view-modes", map[string]string{"f": "seed\n"})
	f := filepath.Join(v.mnt, "f")
	if err := os.Chmod(f, 0o444); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := os.OpenFile(f, os.O_WRONLY, 0); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("open read-only file for write: %v", err)
	}
	if err := unix.Access(f, unix.W_OK); !errors.Is(err, unix.EACCES) {
		t.Fatalf("access(W_OK) on read-only file: %v", err)
	}
	if err := unix.Access(f, unix.R_OK); err != nil {
		t.Fatalf("access(R_OK): %v", err)
	}
	// The descriptor returned by a create keeps its access whatever mode the file was given.
	created, err := os.OpenFile(filepath.Join(v.mnt, "ro-new"), os.O_CREATE|os.O_RDWR, 0o444)
	if err != nil {
		t.Fatalf("create read-only file: %v", err)
	}
	defer func() { _ = created.Close() }()
	if _, err := created.WriteString("owner data"); err != nil {
		t.Fatalf("write through creating descriptor: %v", err)
	}
}

func TestInaccessibleDirtyFileDoesNotWedgeCheckpointAndIsFlushed(t *testing.T) {
	want := []byte("flushed-by-syncfs\n")
	v := prepareMountedView(t, fuselinux.Config{}, "view-inaccessible", map[string]string{"mapped": strings.Repeat("0", len(want))})
	path := filepath.Join(v.mnt, "mapped")
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	mapped, err := syscall.Mmap(int(f.Fd()), 0, len(want), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		t.Fatalf("mmap: %v", err)
	}
	defer func() { _ = syscall.Munmap(mapped) }()
	copyToMapping(mapped, want)
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	rec, release, err := v.adp.CheckpointBarrier(t.Context(), v.viewID)
	if err != nil {
		t.Fatalf("barrier on a mode-000 dirty file: %v", err)
	}
	defer func() { _ = release() }()
	if !rec.DirtyDataFlushed {
		t.Fatalf("receipt: %+v", rec)
	}
	got, err := v.core.ReadFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "mapped"})
	if err != nil || string(got) != string(want) {
		t.Fatalf("mapped bytes after barrier = %q err=%v", got, err)
	}
}

func TestReadOnlyOpensAreNotFlushCandidates(t *testing.T) {
	var mu sync.Mutex
	var synced []string
	v := prepareMountedView(t, fuselinux.Config{}, "view-ro-flush", map[string]string{"read": "r\n", "written": "w\n"})
	if _, err := os.ReadFile(filepath.Join(v.mnt, "read")); err != nil {
		t.Fatalf("read: %v", err)
	}
	held, err := os.Open(filepath.Join(v.mnt, "read"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = held.Close() }()
	if err := os.WriteFile(filepath.Join(v.mnt, "written"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	v.adp.SetConfigCallbacksForTest(func(p string) error {
		mu.Lock()
		defer mu.Unlock()
		synced = append(synced, filepath.Base(p))
		return nil
	}, nil)
	_, release, err := v.adp.CheckpointBarrier(t.Context(), v.viewID)
	if err != nil {
		t.Fatalf("barrier: %v", err)
	}
	_ = release()
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(synced, " ")
	if strings.Contains(joined, "read") || !strings.Contains(joined, "written") {
		t.Fatalf("flush candidates: %s", joined)
	}
}

func TestFlushClientDataInsideActiveBarrierCompletesWithoutWaitingOnItsOwnGate(t *testing.T) {
	v := prepareMountedView(t, fuselinux.Config{}, "view-flush-in-barrier", map[string]string{"f": "seed\n"})
	if err := os.WriteFile(filepath.Join(v.mnt, "f"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cpStore, err := checkpoint.NewStore(filepath.Join(t.TempDir(), "cp"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	callbacks := v.adp.NewBarrierProvider(v.viewID)
	callbacks.FlushClientDirtyData = func(ctx context.Context) error { return v.adp.FlushClientData(ctx, v.viewID) }
	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core: v.core, Store: cpStore, WorkerID: "w", TaskID: "t", ProjectID: "p", SourceGeneration: "g",
		SelectedRoots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: v.trunk, IsPrimary: true}},
		Callbacks:     callbacks,
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	if _, err := pub.Publish(t.Context(), checkpoint.PublishRequest{CheckpointID: "cp", Trigger: checkpoint.TriggerExplicitCapture}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

func TestBusyMountReleaseSettlesWithoutSignalingTheHolder(t *testing.T) {
	v := prepareMountedView(t, fuselinux.Config{}, "view-busy", nil)
	holder := exec.Command("/bin/sh", "-c", "read _; exit 0")
	holder.Dir = v.mnt
	stdin, err := holder.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	if err := holder.Start(); err != nil {
		t.Fatalf("start holder: %v", err)
	}
	rec, err := v.adp.Release(t.Context(), v.viewID)
	if err != nil || !rec.Settled {
		t.Fatalf("Release of busy mount: rec=%+v err=%v", rec, err)
	}
	if err := holder.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("holder did not survive Release: %v", err)
	}
	_ = stdin.Close()
	if err := holder.Wait(); err != nil {
		t.Fatalf("holder ended abnormally: %v", err)
	}
}

func TestForgedConnectionIdentityIsNotTreatedAsOwnedMount(t *testing.T) {
	v := prepareMountedView(t, fuselinux.Config{}, "view-identity", nil)
	if !v.adp.IsViewActive(v.viewID) {
		t.Fatal("view should start active")
	}
	mf, _ := v.adp.MountFactsForView(v.viewID)
	v.adp.ForgeMountDeviceForTest(v.viewID, "0:1")
	if v.adp.IsViewActive(v.viewID) {
		t.Fatal("a mount with another connection's device matched by path and source alone")
	}
	v.adp.ForgeMountDeviceForTest(v.viewID, mf.DevMajorMinor)
	if !v.adp.IsViewActive(v.viewID) {
		t.Fatal("restored identity should match")
	}
}

// TestReleaseNeverDeletesThroughAMountItCouldNotRemove leaves a mount in place by giving Release a
// wrong identity for it. Runtime cleanup must refuse instead of recursing into the mount, and the
// dead mount is then reclaimable so a retry settles.
func TestReleaseNeverDeletesThroughAMountItCouldNotRemove(t *testing.T) {
	v := prepareMountedView(t, fuselinux.Config{}, "view-unremovable", map[string]string{"keep": "kept\n"})
	if err := os.WriteFile(filepath.Join(v.mnt, "private"), []byte("private\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	v.adp.ForgeMountDeviceForTest(v.viewID, "0:1")
	rec, err := v.adp.Release(t.Context(), v.viewID)
	var aErr *adapter.AdapterError
	if !errors.As(err, &aErr) || aErr.ReasonCode != adapter.ReasonCleanupFailed || rec.Settled {
		t.Fatalf("expected cleanup_failed with a mount remaining, got rec=%+v err=%v", rec, err)
	}
	reclaimed, err := v.adp.ReclaimDeadMounts(t.Context(), v.runtimeDir)
	if err != nil || len(reclaimed) != 1 {
		t.Fatalf("reclaim after failed release: %v err=%v", reclaimed, err)
	}
	if rec, err := v.adp.Release(t.Context(), v.viewID); err != nil || !rec.Settled {
		t.Fatalf("retry after reclaim: rec=%+v err=%v", rec, err)
	}
}

func TestMultiRootViewServesEachRootPrivatelyAndSettles(t *testing.T) {
	adp := fuselinux.New(fuselinux.Config{})
	tmp := t.TempDir()
	first, second := filepath.Join(tmp, "first"), filepath.Join(tmp, "second")
	for _, d := range []string{first, second} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(d, "f"), []byte(filepath.Base(d)), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	const viewID = "view-multi-root"
	_, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: viewID, BackingDir: filepath.Join(tmp, "backing"), RuntimeDir: filepath.Join(tmp, "runtime"),
		Grants: []adapter.RootGrant{
			{RootID: "first", Label: "first", SourcePath: first, IsPrimary: true},
			{RootID: "second", Label: "second", SourcePath: second},
		},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(context.Background(), viewID) })
	access, err := adp.CommandAccess(viewID)
	if err != nil || len(access.RootPaths) != 2 || len(access.DenyDirectSource) != 2 || access.HiddenBackingDir == "" {
		t.Fatalf("CommandAccess: %+v err=%v", access, err)
	}
	for rootID, trunk := range map[string]string{"first": first, "second": second} {
		mounted := filepath.Join(access.RootPaths[rootID], "f")
		if got, err := os.ReadFile(mounted); err != nil || string(got) != rootID {
			t.Fatalf("root %s reads %q err=%v", rootID, got, err)
		}
		if err := os.WriteFile(mounted, []byte("private-"+rootID), 0o644); err != nil {
			t.Fatalf("write root %s: %v", rootID, err)
		}
		if got, err := os.ReadFile(filepath.Join(trunk, "f")); err != nil || string(got) != rootID {
			t.Fatalf("trunk of root %s changed: %q err=%v", rootID, got, err)
		}
	}
	if rec, err := adp.Release(t.Context(), viewID); err != nil || !rec.Settled {
		t.Fatalf("Release: rec=%+v err=%v", rec, err)
	}
	for rootID, mountPath := range access.RootPaths {
		if _, err := os.Stat(mountPath); !os.IsNotExist(err) {
			t.Fatalf("root %s mount path survives Release: %v", rootID, err)
		}
	}
}

func TestMountTimeoutBoundsTheHelper(t *testing.T) {
	adp := fuselinux.New(fuselinux.Config{MountTimeout: 1})
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	if err := os.MkdirAll(trunk, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: "view-timeout", BackingDir: filepath.Join(tmp, "backing"), RuntimeDir: filepath.Join(tmp, "runtime"),
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunk, IsPrimary: true}},
	})
	var aErr *adapter.AdapterError
	if !errors.As(err, &aErr) || aErr.ReasonCode != adapter.ReasonContextCanceled {
		t.Fatalf("expected ReasonContextCanceled from expired mount timeout, got %v", err)
	}
}

// TestInvalidationNeverBlocksRepliesItsKernelWorkWaitsOn runs directory mutations, which hold the
// parent's inode lock until answered, against entry invalidations that need that same lock.
func TestInvalidationNeverBlocksRepliesItsKernelWorkWaitsOn(t *testing.T) {
	v := prepareMountedView(t, fuselinux.Config{}, "view-invalidate-race", nil)
	dir := filepath.Join(v.mnt, "dir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const name = "subject"
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	stop := make(chan struct{})
	var invalidator sync.WaitGroup
	invalidator.Go(func() {
		qp := layeredfs.QualifiedPath{RootID: "primary", Rel: "dir/" + name}
		for {
			select {
			case <-stop:
				return
			default:
				_ = v.adp.InvalidatePath(v.viewID, qp)
			}
		}
	})
	defer func() {
		close(stop)
		invalidator.Wait()
	}()
	for i := range 500 {
		other := filepath.Join(dir, fmt.Sprintf("other-%d", i))
		if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		if err := os.Remove(other); err != nil {
			t.Fatalf("remove %d: %v", i, err)
		}
	}
}

// TestDetachedMountEntrySurvivesStopTheWorld repeats the sequence that hangs an undetached child:
// a garbage collection is always pending while children enter the mount and run a program from it.
func TestDetachedMountEntrySurvivesStopTheWorld(t *testing.T) {
	tool, err := os.ReadFile("/bin/true")
	if err != nil {
		t.Fatalf("read tool: %v", err)
	}
	v := prepareMountedView(t, fuselinux.Config{}, "view-detached-entry", map[string]string{"tool": string(tool)})
	if err := os.Chmod(filepath.Join(v.mnt, "tool"), 0o755); err != nil {
		t.Fatalf("chmod tool: %v", err)
	}
	sub := filepath.Join(v.mnt, "sub", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stop := make(chan struct{})
	var collector sync.WaitGroup
	collector.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				runtime.GC()
			}
		}
	})
	defer func() {
		close(stop)
		collector.Wait()
	}()
	for i := range 300 {
		cmd := exec.Command(filepath.Join(v.mnt, "tool"))
		cmd.Dir = sub
		fuselinux.DetachMountEntry(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("iteration %d: %v (%s)", i, err, out)
		}
	}
	cwd := exec.Command("/bin/pwd", "-P")
	cwd.Dir = sub
	fuselinux.DetachMountEntry(cwd)
	if out, err := cwd.Output(); err != nil || strings.TrimSpace(string(out)) != sub {
		t.Fatalf("working directory %q err=%v, want %s", out, err, sub)
	}
}

func TestInvalidatePathRefreshesAHeldDescriptorsPages(t *testing.T) {
	v := prepareMountedView(t, fuselinux.Config{}, "view-invalidate", map[string]string{"f": "AAAA"})
	f, err := os.OpenFile(filepath.Join(v.mnt, "f"), os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 4)
	if _, err := f.ReadAt(buf, 0); err != nil || string(buf) != "AAAA" {
		t.Fatalf("first read %q err=%v", buf, err)
	}
	qp := layeredfs.QualifiedPath{RootID: "primary", Rel: "f"}
	hid, _, err := v.core.Open(qp, layeredfs.OpenReadWrite, 0)
	if err != nil {
		t.Fatalf("server-side open: %v", err)
	}
	if _, err := v.core.WriteHandle(hid, []byte("BBBB"), 0); err != nil {
		t.Fatalf("server-side write: %v", err)
	}
	if err := v.core.CloseHandle(hid); err != nil {
		t.Fatalf("server-side close: %v", err)
	}
	if err := v.adp.InvalidatePath(v.viewID, qp); err != nil {
		t.Fatalf("InvalidatePath: %v", err)
	}
	if _, err := f.ReadAt(buf, 0); err != nil || string(buf) != "BBBB" {
		t.Fatalf("read after invalidation %q err=%v", buf, err)
	}
}

// TestEngineDeathLeavesReclaimableMountAndOnlyDeadOnesAreReclaimed kills a real serving process.
// Bystanders in the dead mount keep running, and a live view in the same runtime root is untouched.
func TestEngineDeathLeavesReclaimableMountAndOnlyDeadOnesAreReclaimed(t *testing.T) {
	if os.Getenv("LYCAON_FUSELINUX_ENGINE_HELPER") != "" {
		runEngineHelper(t)
		return
	}
	tmp := t.TempDir()
	runtimeRoot := filepath.Join(tmp, "runtime")
	trunk := filepath.Join(tmp, "trunk")
	if err := os.MkdirAll(trunk, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	engine := exec.Command(exe, "-test.run=^TestEngineDeathLeavesReclaimableMountAndOnlyDeadOnesAreReclaimed$")
	engine.Env = append(os.Environ(), "LYCAON_FUSELINUX_ENGINE_HELPER="+tmp)
	stdout, err := engine.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	stdin, err := engine.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	defer func() { _ = stdin.Close() }()
	engine.Stderr = os.Stderr
	if err := engine.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	var deadMount string
	if _, err := fmt.Fscanf(stdout, "%s\n", &deadMount); err != nil {
		t.Fatalf("engine helper did not report its mount: %v", err)
	}
	t.Cleanup(func() {
		_ = engine.Process.Kill()
		_ = exec.Command("fusermount3", "-u", "-z", "--", deadMount).Run()
	})

	bystander := exec.Command("/bin/sh", "-c", "read _; exit 0")
	bystander.Dir = deadMount
	bystanderIn, err := bystander.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	if err := bystander.Start(); err != nil {
		t.Fatalf("start bystander: %v", err)
	}

	adp := fuselinux.New(fuselinux.Config{})
	live, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: "view-live", BackingDir: filepath.Join(tmp, "live-backing"), RuntimeDir: runtimeRoot,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunk, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare live view: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(context.Background(), "view-live") })

	if none, err := adp.ReclaimDeadMounts(t.Context(), runtimeRoot); err != nil || len(none) != 0 {
		t.Fatalf("reclaim with no dead mounts: %v err=%v", none, err)
	}

	if err := engine.Process.Kill(); err != nil {
		t.Fatalf("kill engine: %v", err)
	}
	_ = engine.Wait()
	var st unix.Statfs_t
	if err := unix.Statfs(deadMount, &st); !errors.Is(err, unix.ENOTCONN) {
		t.Fatalf("dead mount statfs: %v", err)
	}

	reclaimed, err := adp.ReclaimDeadMounts(t.Context(), runtimeRoot)
	if err != nil || len(reclaimed) != 1 || reclaimed[0] != deadMount {
		t.Fatalf("reclaimed %v err=%v, want only %s", reclaimed, err, deadMount)
	}
	if err := unix.Statfs(deadMount, &st); errors.Is(err, unix.ENOTCONN) {
		t.Fatalf("dead mount still attached")
	}
	if err := bystander.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("bystander was affected by engine death or reclaim: %v", err)
	}
	if _, err := os.ReadDir(live.PrimaryWorkPath); err != nil {
		t.Fatalf("live view affected by reclaim: %v", err)
	}
	_ = bystanderIn.Close()
	if err := bystander.Wait(); err != nil {
		t.Fatalf("bystander: %v", err)
	}
}

func runEngineHelper(t *testing.T) {
	tmp := os.Getenv("LYCAON_FUSELINUX_ENGINE_HELPER")
	adp := fuselinux.New(fuselinux.Config{})
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: "view-dead", BackingDir: filepath.Join(tmp, "dead-backing"), RuntimeDir: filepath.Join(tmp, "runtime"),
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: filepath.Join(tmp, "trunk"), IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("helper Prepare: %v", err)
	}
	fmt.Println(prep.PrimaryWorkPath)
	// Serve until killed; reading stdin ends only if the parent test itself goes away.
	_, _ = os.Stdin.Read(make([]byte, 1))
	_, _ = adp.Release(context.Background(), "view-dead")
}
