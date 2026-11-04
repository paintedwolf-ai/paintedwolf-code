//go:build linux

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
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/fuselinux"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"golang.org/x/sys/unix"
)

func confinementVerified(context.Context, adapter.ProbeConfig) error { return nil }

func TestAdapterFactsAndFailureClassifications(t *testing.T) {
	adp := fuselinux.New(fuselinux.Config{HostOS: "darwin"})
	if adp.ID() != adapter.AdapterFUSELinux || adp.Name() != adapter.AdapterFUSELinux {
		t.Fatalf("expected ID=%q, got ID=%q Name=%q", adapter.AdapterFUSELinux, adp.ID(), adp.Name())
	}
	facts := adp.Facts()
	if facts.PreparationKind != adapter.PreparationMounted || facts.UntouchedSourceVisibility != adapter.VisibilityLive ||
		!facts.CacheRevalidation.ExplicitInvalidation || facts.CacheRevalidation.AttributeTTL != 0 ||
		!facts.SupportsPrivateGit || !facts.SupportsCheckpointBarrier {
		t.Fatalf("unexpected AdapterFacts: %+v", facts)
	}
	opts := fuselinux.FormatMountOptions("lycaon-v1-primary")
	if strings.Contains(opts, "allow_other") || !strings.Contains(opts, "subtype="+fuselinux.DefaultSubtype) {
		t.Fatalf("unexpected mount options: %q", opts)
	}

	if res, err := adp.Probe(t.Context(), adapter.ProbeConfig{}); err == nil || res.Supported || res.ReasonCode != adapter.ReasonUnsupportedPlatform {
		t.Fatalf("expected ReasonUnsupportedPlatform on darwin HostOS, got res=%+v err=%v", res, err)
	}

	confAdp := fuselinux.New(fuselinux.Config{HostOS: "linux"})
	if res, err := confAdp.Probe(t.Context(), adapter.ProbeConfig{ConfinementContext: string(adapter.ReasonConfinementUnavailable)}); err == nil || res.ReasonCode != adapter.ReasonConfinementUnavailable {
		t.Fatalf("expected ReasonConfinementUnavailable from ConfinementContext, got res=%+v err=%v", res, err)
	}

	confFnAdp := fuselinux.New(fuselinux.Config{
		HostOS: "linux",
		ConfinementCheckFn: func(context.Context, adapter.ProbeConfig) error {
			return errors.New("bwrap/ns unavailable")
		},
	})
	if res, err := confFnAdp.Probe(t.Context(), adapter.ProbeConfig{}); err == nil || res.ReasonCode != adapter.ReasonConfinementUnavailable {
		t.Fatalf("expected ReasonConfinementUnavailable from ConfinementCheckFn, got res=%+v err=%v", res, err)
	}

	// Without a check, backing denial is unknown: the probe must not report it as verified.
	noCheckAdp := fuselinux.New(fuselinux.Config{HostOS: "linux"})
	if res, err := noCheckAdp.Probe(t.Context(), adapter.ProbeConfig{}); err == nil || res.Supported ||
		res.BackingDenialVerified || res.ReasonCode != adapter.ReasonConfinementUnavailable {
		t.Fatalf("expected ReasonConfinementUnavailable with no confinement check, got res=%+v err=%v", res, err)
	}

	noDevAdp := fuselinux.New(fuselinux.Config{HostOS: "linux", ConfinementCheckFn: confinementVerified, FuseDevicePath: "/nonexistent/dev/fuse"})
	if res, err := noDevAdp.Probe(t.Context(), adapter.ProbeConfig{}); err == nil || res.ReasonCode != adapter.ReasonFUSEDeviceUnavailable {
		t.Fatalf("expected ReasonFUSEDeviceUnavailable for missing /dev/fuse, got res=%+v err=%v", res, err)
	}

	if runtime.GOOS == "linux" {
		noHelperAdp := fuselinux.New(fuselinux.Config{HostOS: "linux", ConfinementCheckFn: confinementVerified, FusermountBin: "/nonexistent/bin/fusermount3"})
		if res, err := noHelperAdp.Probe(t.Context(), adapter.ProbeConfig{}); err == nil || res.ReasonCode != adapter.ReasonFUSEHelperUnavailable {
			t.Fatalf("expected ReasonFUSEHelperUnavailable for missing fusermount3, got res=%+v err=%v", res, err)
		}
		deniedAdp := fuselinux.New(fuselinux.Config{
			HostOS:             "linux",
			ConfinementCheckFn: confinementVerified,
			MountHookForTest: func(_, _, _ string) error {
				return errors.New("simulated EPERM mount denial")
			},
		})
		if res, err := deniedAdp.Probe(t.Context(), adapter.ProbeConfig{}); err == nil || res.ReasonCode != adapter.ReasonFUSEMountDenied {
			t.Fatalf("expected ReasonFUSEMountDenied from MountHookForTest, got res=%+v err=%v", res, err)
		}
	}
}

func TestProbeAndPrepareFUSELinux(t *testing.T) {

	adp := fuselinux.New(fuselinux.Config{ConfinementCheckFn: confinementVerified})
	tmp := t.TempDir()
	probeRes, err := adp.Probe(t.Context(), adapter.ProbeConfig{
		RuntimeDir: filepath.Join(tmp, "probe-runtime"),
	})
	if err != nil || !probeRes.Supported || probeRes.ReasonCode != adapter.ReasonNone ||
		!probeRes.MountExercised || !probeRes.ReadExercised || !probeRes.WriteExercised ||
		!probeRes.BackingDenialVerified || !probeRes.CleanedUp {
		t.Fatalf("expected Probe to succeed on linux, got %+v err=%v", probeRes, err)
	}

	trunkDir := filepath.Join(tmp, "trunk")
	backingDir := filepath.Join(tmp, "backing")
	runtimeDir := filepath.Join(tmp, "runtime")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "hello.txt"), []byte("from-trunk\n"), 0o644); err != nil {
		t.Fatalf("seed hello.txt: %v", err)
	}

	const viewID = "view-prepare-linux-1"
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID:     viewID,
		BackingDir: backingDir,
		RuntimeDir: runtimeDir,
		Grants:     []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(t.Context(), viewID) })

	mf, ok := adp.MountFactsForView(viewID)
	if !ok {
		t.Fatalf("expected MountFactsForView for %s", viewID)
	}
	if mf.FilesystemType != "fuse."+fuselinux.DefaultSubtype || mf.ProtocolMajor != 7 || mf.ProtocolMinor != 31 {
		t.Fatalf("unexpected MountFacts: %+v", mf)
	}
	if strings.Contains(mf.MountOptions, "allow_other") || strings.Contains(mf.SuperOptions, "allow_other") {
		t.Fatalf("forbidden allow_other in MountFacts: %+v", mf)
	}
	wantUID := fmt.Sprintf("user_id=%d", os.Getuid())
	wantGID := fmt.Sprintf("group_id=%d", os.Getgid())
	if !strings.Contains(mf.SuperOptions, wantUID) || !strings.Contains(mf.SuperOptions, wantGID) {
		t.Fatalf("expected %s and %s in SuperOptions %q", wantUID, wantGID, mf.SuperOptions)
	}
	if mf.MountedDev == 0 || mf.MountedDev == mf.ParentDev {
		t.Fatalf("expected MountedDev (%d) != ParentDev (%d)", mf.MountedDev, mf.ParentDev)
	}

	access, err := adp.CommandAccess(viewID)
	if err != nil || access.WorkDir != prep.PrimaryWorkPath {
		t.Fatalf("CommandAccess failed: access=%+v err=%v", access, err)
	}

	relReceipt, err := adp.Release(t.Context(), viewID)
	if err != nil || !relReceipt.Settled || len(relReceipt.CleanedPaths) == 0 {
		t.Fatalf("Release failed: receipt=%+v err=%v", relReceipt, err)
	}
}

func TestImmediateCoherenceAndBackgroundProcessStability(t *testing.T) {

	adp := fuselinux.New(fuselinux.Config{})
	tmp := t.TempDir()
	trunkDir := filepath.Join(tmp, "trunk")
	backingDir := filepath.Join(tmp, "backing")
	runtimeDir := filepath.Join(tmp, "runtime")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "hello.txt"), []byte("AAAA\n"), 0o644); err != nil {
		t.Fatalf("seed hello.txt: %v", err)
	}

	const viewID = "view-coherence-linux"
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID:     viewID,
		BackingDir: backingDir,
		RuntimeDir: runtimeDir,
		Grants:     []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(t.Context(), viewID) })

	mnt := prep.PrimaryWorkPath

	// Start a long-lived background process (simulating a language server / watcher) with cwd = mnt.
	bgCtx, stopBG := context.WithCancel(t.Context())
	defer stopBG()
	bgCmd := exec.CommandContext(bgCtx, "/bin/sh", "-c",
		"while [ ! -f stop.flag ]; do if [ -f watch.in ]; then cp watch.in watch.out; fi; sleep 0.02; done")
	bgCmd.Dir = mnt
	fuselinux.DetachMountEntry(bgCmd)
	if err := bgCmd.Start(); err != nil {
		t.Fatalf("start background process: %v", err)
	}
	defer func() {
		_ = os.WriteFile(filepath.Join(mnt, "stop.flag"), []byte("1"), 0o644)
		_ = bgCmd.Wait()
	}()

	// Verify same-size native overwrite is immediately visible to shell without sleep.
	if err := os.WriteFile(filepath.Join(mnt, "hello.txt"), []byte("BBBB\n"), 0o644); err != nil {
		t.Fatalf("write hello.txt: %v", err)
	}
	shCmd := exec.CommandContext(t.Context(), "/bin/sh", "-c",
		"cat hello.txt && printf 'CCCC\n' > hello.txt && ln hello.txt hardlink.txt && ln -s hello.txt symlink.txt")
	shCmd.Dir = mnt
	fuselinux.DetachMountEntry(shCmd)
	out, err := shCmd.CombinedOutput()
	if err != nil || string(out) != "BBBB\n" {
		t.Fatalf("shell coherence failed: err=%v out=%q", err, string(out))
	}

	// Verify shell overwrite, hardlink st_ino equality, and symlink readlink from Go immediately.
	gotHello, err := os.ReadFile(filepath.Join(mnt, "hello.txt"))
	if err != nil || string(gotHello) != "CCCC\n" {
		t.Fatalf("expected CCCC from Go read, got %q err=%v", string(gotHello), err)
	}
	st1, err1 := os.Stat(filepath.Join(mnt, "hello.txt"))
	st2, err2 := os.Stat(filepath.Join(mnt, "hardlink.txt"))
	if err1 != nil || err2 != nil || !os.SameFile(st1, st2) {
		t.Fatalf("expected hardlink.txt and hello.txt to share st_ino: err1=%v err2=%v same=%v", err1, err2, os.SameFile(st1, st2))
	}
	if linkTarget, err := os.Readlink(filepath.Join(mnt, "symlink.txt")); err != nil || linkTarget != "hello.txt" {
		t.Fatalf("expected symlink target hello.txt, got %q err=%v", linkTarget, err)
	}

	// Verify chmod, chtimes, xattr, and flock on the FUSE mount.
	if err := os.Chmod(filepath.Join(mnt, "hello.txt"), 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := unix.Setxattr(filepath.Join(mnt, "hello.txt"), "user.lycaon", []byte("xval"), 0); err != nil {
		t.Fatalf("setxattr: %v", err)
	}
	xbuf := make([]byte, 16)
	xn, err := unix.Getxattr(filepath.Join(mnt, "hello.txt"), "user.lycaon", xbuf)
	if err != nil || string(xbuf[:xn]) != "xval" {
		t.Fatalf("getxattr failed: got %q err=%v", string(xbuf[:xn]), err)
	}
	lf, err := os.OpenFile(filepath.Join(mnt, "hello.txt"), os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open for flock: %v", err)
	}
	if err := unix.Flock(int(lf.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lf.Close()
		t.Fatalf("flock LOCK_EX: %v", err)
	}
	_ = unix.Flock(int(lf.Fd()), unix.LOCK_UN)
	_ = lf.Close()

	// Verify background process observed native edits in place without path change.
	if err := os.WriteFile(filepath.Join(mnt, "watch.in"), []byte("lsp-coherent-v1\n"), 0o644); err != nil {
		t.Fatalf("write watch.in: %v", err)
	}
	awaitCondition(t, "background process copied watch.in", func() bool {
		raw, err := os.ReadFile(filepath.Join(mnt, "watch.out"))
		return err == nil && string(raw) == "lsp-coherent-v1\n"
	})
	_ = os.WriteFile(filepath.Join(mnt, "stop.flag"), []byte("1"), 0o644)
	_ = bgCmd.Wait()
}

func TestBarrierFailsOnDeadServerAndReleaseRefusesUnownedPath(t *testing.T) {

	adp := fuselinux.New(fuselinux.Config{})
	tmp := t.TempDir()
	trunkDir, backingDir, runtimeDir, cpStoreDir := filepath.Join(tmp, "trunk"), filepath.Join(tmp, "backing"), filepath.Join(tmp, "runtime"), filepath.Join(tmp, "cp")
	for _, d := range []string{trunkDir, backingDir, runtimeDir, cpStoreDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("seed base.txt: %v", err)
	}

	const viewID = "view-dead-fuse"
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: viewID, BackingDir: backingDir, RuntimeDir: runtimeDir,
		QuiesceHook: fuselinux.NoopQuiesceHook, FlushClientHook: fuselinux.NoWritableMappingsFlushHook,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(t.Context(), viewID) })

	if err := os.WriteFile(filepath.Join(prep.PrimaryWorkPath, "step1.txt"), []byte("cp1\n"), 0o644); err != nil {
		t.Fatalf("write step1.txt: %v", err)
	}
	cpStore, err := checkpoint.NewStore(cpStoreDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core: prep.Core, Store: cpStore, WorkerID: "w1", TaskID: "t1", ProjectID: "p1", SourceGeneration: "g1",
		SelectedRoots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunkDir, IsPrimary: true}},
		Callbacks: checkpoint.BarrierCallbacks{
			FlushClientDirtyData: func(ctx context.Context) error { return adp.FlushClientData(ctx, viewID) },
		},
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	if _, err := pub.Publish(t.Context(), checkpoint.PublishRequest{CheckpointID: "cp-1", Trigger: checkpoint.TriggerExplicitCapture}); err != nil {
		t.Fatalf("Publish cp-1: %v", err)
	}

	if err := adp.KillServerForView(viewID); err != nil {
		t.Fatalf("KillServerForView: %v", err)
	}
	if _, _, err := adp.CheckpointBarrier(t.Context(), viewID); err == nil {
		t.Fatalf("expected CheckpointBarrier to fail after server death")
	}
	if loaded, err := cpStore.LoadManifest("cp-1"); err != nil || loaded.CheckpointID != "cp-1" {
		t.Fatalf("expected cp-1 preserved after dead server barrier failure, got %+v err=%v", loaded, err)
	}
	if rec, err := adp.Release(t.Context(), viewID); err != nil || !rec.Settled {
		t.Fatalf("expected Release to clean up dead FUSE mount, got rec=%+v err=%v", rec, err)
	}
}
