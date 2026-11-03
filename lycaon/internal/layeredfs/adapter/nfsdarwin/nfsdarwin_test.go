package nfsdarwin_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/nfsdarwin"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
)

func TestProbeAndPrepareNFSDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("nfsdarwin requires darwin")
	}

	nonDarwin := nfsdarwin.New(nfsdarwin.Config{HostOS: "linux"})
	if res, err := nonDarwin.Probe(t.Context(), adapter.ProbeConfig{}); err == nil || res.Supported || res.ReasonCode != adapter.ReasonUnsupportedPlatform {
		t.Fatalf("expected unsupported platform on linux HostOS, got res=%+v err=%v", res, err)
	}

	missingBin := nfsdarwin.New(nfsdarwin.Config{MountNFSBin: "/nonexistent/sbin/mount_nfs"})
	if res, err := missingBin.Probe(t.Context(), adapter.ProbeConfig{}); err == nil || res.Supported || res.ReasonCode != adapter.ReasonHelperMissing {
		t.Fatalf("expected helper_missing for bad MountNFSBin, got res=%+v err=%v", res, err)
	}

	adp := nfsdarwin.New(nfsdarwin.Config{})
	if adp.ID() != adapter.AdapterNFSDarwin || adp.Name() != adapter.AdapterNFSDarwin {
		t.Fatalf("expected adapter ID %q, got ID=%q Name=%q", adapter.AdapterNFSDarwin, adp.ID(), adp.Name())
	}
	if adp.Facts().CacheRevalidation.ExplicitInvalidation {
		t.Fatalf("expected ExplicitInvalidation=false for NFSv3 loopback adapter")
	}

	tmp := t.TempDir()
	probeRes, err := adp.Probe(t.Context(), adapter.ProbeConfig{
		RuntimeDir: filepath.Join(tmp, "probe-runtime"),
	})
	if err != nil || !probeRes.Supported || probeRes.ReasonCode != adapter.ReasonNone || !probeRes.MountExercised || !probeRes.ReadExercised || !probeRes.WriteExercised || !probeRes.CleanedUp {
		t.Fatalf("expected Probe to succeed on darwin, got %+v err=%v", probeRes, err)
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

	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID:     "view-prepare-1",
		BackingDir: backingDir,
		RuntimeDir: runtimeDir,
		Grants:     []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(t.Context(), "view-prepare-1") })

	facts, ok := adp.MountFactsForView("view-prepare-1")
	if !ok {
		t.Fatalf("expected MountFactsForView to exist")
	}
	if facts.RSize != 65536 || facts.WSize != 65536 || facts.ServerPort <= 0 || facts.MountPort != facts.ServerPort {
		t.Fatalf("unexpected MountFacts: %+v", facts)
	}
	for _, wantOpt := range []string{"vers=3", "tcp", "nolocks", "locallocks", "nodev", "nosuid", "nobrowse", "actimeo=0", "nonegnamecache", "retrans=2", "timeo=10", "soft", "rsize=65536,wsize=65536"} {
		if !strings.Contains(facts.MountOptions, wantOpt) {
			t.Fatalf("missing option %q in MountOptions %q", wantOpt, facts.MountOptions)
		}
	}
	if facts.MountedDev == 0 || facts.MountedDev == facts.ParentDev {
		t.Fatalf("expected MountedDev (%d) != ParentDev (%d)", facts.MountedDev, facts.ParentDev)
	}
	if facts.AllowedDataUID != uint32(os.Getuid()) || facts.AllowedDataGID != uint32(os.Getgid()) {
		t.Fatalf("unexpected AllowedDataUID/GID in MountFacts: %+v", facts)
	}
	srv := adp.ServerForView("view-prepare-1")
	if obs, ok := srv.LastMountHandshake(facts.ExportPath); !ok || !obs.Mounted || obs.AllowedUID != uint32(os.Getuid()) {
		t.Fatalf("expected recorded MountHandshakeObservation on server, got ok=%v obs=%+v", ok, obs)
	}

	access, err := adp.CommandAccess("view-prepare-1")
	if err != nil || access.WorkDir != prep.PrimaryWorkPath {
		t.Fatalf("CommandAccess failed: access=%+v err=%v", access, err)
	}

	relReceipt, err := adp.Release(t.Context(), "view-prepare-1")
	if err != nil || !relReceipt.Settled {
		t.Fatalf("Release failed: receipt=%+v err=%v", relReceipt, err)
	}
}

func TestImmediateCoherenceAndNegativeLookup(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("nfsdarwin requires darwin")
	}

	adp := nfsdarwin.New(nfsdarwin.Config{})
	tmp := t.TempDir()
	trunkDir := filepath.Join(tmp, "trunk")
	backingDir := filepath.Join(tmp, "backing")
	runtimeDir := filepath.Join(tmp, "runtime")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "hello.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("seed hello.txt: %v", err)
	}

	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID:     "view-coherence",
		BackingDir: backingDir,
		RuntimeDir: runtimeDir,
		Grants:     []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(t.Context(), "view-coherence") })

	mnt := prep.PrimaryWorkPath
	negFile := filepath.Join(mnt, "created-after-negative-lookup.txt")

	if _, err := os.Stat(negFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist before creation, got %v", err)
	}
	negCmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", "test ! -e created-after-negative-lookup.txt")
	negCmd.Dir = mnt
	if out, err := negCmd.CombinedOutput(); err != nil {
		t.Fatalf("negative lookup shell check failed: %v (%s)", err, string(out))
	}

	if err := os.WriteFile(negFile, []byte("neg-resolved\n"), 0o644); err != nil {
		t.Fatalf("write negFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mnt, "hello.txt"), []byte("v2-from-go\n"), 0o644); err != nil {
		t.Fatalf("write hello.txt: %v", err)
	}

	shCmd := exec.CommandContext(t.Context(), "/bin/sh", "-c",
		"cat created-after-negative-lookup.txt && cat hello.txt && printf 'v3-from-shell\n' > hello.txt && printf 'shell-new\n' > from-shell.txt")
	shCmd.Dir = mnt
	out, err := shCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shell read/write failed: %v (%s)", err, string(out))
	}
	if string(out) != "neg-resolved\nv2-from-go\n" {
		t.Fatalf("unexpected shell output: %q", string(out))
	}

	gotHello, err := os.ReadFile(filepath.Join(mnt, "hello.txt"))
	if err != nil || string(gotHello) != "v3-from-shell\n" {
		t.Fatalf("expected v3-from-shell from Go read, got %q err=%v", string(gotHello), err)
	}
	gotShellNew, err := os.ReadFile(filepath.Join(mnt, "from-shell.txt"))
	if err != nil || string(gotShellNew) != "shell-new\n" {
		t.Fatalf("expected shell-new from Go read, got %q err=%v", string(gotShellNew), err)
	}

	trunkHello, err := os.ReadFile(filepath.Join(trunkDir, "hello.txt"))
	if err != nil || string(trunkHello) != "v1\n" {
		t.Fatalf("trunk hello.txt was mutated: %q err=%v", string(trunkHello), err)
	}
	if _, err := os.Stat(filepath.Join(trunkDir, "from-shell.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("trunk should not contain from-shell.txt")
	}
	snap, err := prep.Core.SnapshotJournal()
	if err != nil || len(snap.Operations) == 0 {
		t.Fatalf("expected non-empty Core journal operations after mounted writes, got len=%d err=%v", len(snap.Operations), err)
	}
}

func TestBarrierFailsOnDeadServerAndPreservesEarlierCheckpoints(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("nfsdarwin requires darwin")
	}

	adp := nfsdarwin.New(nfsdarwin.Config{})
	tmp := t.TempDir()
	trunkDir := filepath.Join(tmp, "trunk")
	backingDir := filepath.Join(tmp, "backing")
	runtimeDir := filepath.Join(tmp, "runtime")
	cpStoreDir := filepath.Join(tmp, "cp-store")
	for _, d := range []string{trunkDir, backingDir, runtimeDir, cpStoreDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("seed base.txt: %v", err)
	}

	const viewID = "view-dead-server"
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID:          viewID,
		BackingDir:      backingDir,
		RuntimeDir:      runtimeDir,
		QuiesceHook:     nfsdarwin.NoopQuiesceHook,
		FlushClientHook: nfsdarwin.NoWritableMappingsFlushHook,
		Grants:          []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(t.Context(), viewID) })

	if err := os.WriteFile(filepath.Join(prep.PrimaryWorkPath, "step1.txt"), []byte("checkpoint-one\n"), 0o644); err != nil {
		t.Fatalf("write step1.txt: %v", err)
	}

	cpStore, err := checkpoint.NewStore(cpStoreDir)
	if err != nil {
		t.Fatalf("checkpoint.NewStore: %v", err)
	}
	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core:             prep.Core,
		Store:            cpStore,
		WorkerID:         "worker-1",
		TaskID:           "task-1",
		ProjectID:        "proj-1",
		SourceGeneration: "gen-1",
		SelectedRoots:    []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunkDir, IsPrimary: true}},
		Callbacks: checkpoint.BarrierCallbacks{
			FlushClientDirtyData: func(ctx context.Context) error {
				return adp.FlushClientData(ctx, viewID)
			},
		},
	})
	if err != nil {
		t.Fatalf("checkpoint.NewPublisher: %v", err)
	}

	cp1, err := pub.Publish(t.Context(), checkpoint.PublishRequest{
		CheckpointID: "cp-1",
		Trigger:      checkpoint.TriggerExplicitCapture,
	})
	if err != nil || cp1.Manifest.CheckpointID != "cp-1" {
		t.Fatalf("Publish cp-1 failed: %v", err)
	}

	if err := adp.KillServerForView(viewID); err != nil {
		t.Fatalf("KillServerForView: %v", err)
	}

	if _, _, bErr := adp.CheckpointBarrier(t.Context(), viewID); bErr == nil {
		t.Fatalf("expected CheckpointBarrier to fail when loopback NFS server is dead")
	}
	if _, pErr := pub.Publish(t.Context(), checkpoint.PublishRequest{
		CheckpointID: "cp-2",
		Trigger:      checkpoint.TriggerExplicitCapture,
	}); pErr == nil {
		t.Fatalf("expected Publish cp-2 to fail when loopback NFS server is dead")
	}

	loadedCP1, err := cpStore.LoadManifest("cp-1")
	if err != nil || loadedCP1.CheckpointID != "cp-1" {
		t.Fatalf("expected cp-1 manifest preserved, got %+v err=%v", loadedCP1, err)
	}
	if _, err := adp.Release(t.Context(), viewID); err != nil {
		t.Fatalf("expected Release to force-unmount cleanly after server death, got %v", err)
	}
	if _, err := os.Stat(backingDir); err != nil {
		t.Fatalf("expected BackingDir preserved after Release, got %v", err)
	}
	if _, err := prep.Core.ReadFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "step1.txt"}); !errors.Is(err, layeredfs.ErrViewReleased) {
		t.Fatalf("expected Core to be released after Release, got %v", err)
	}
}

func TestReleaseDoesNotUnmountUnownedPath(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("nfsdarwin requires darwin")
	}

	umountLog := filepath.Join(t.TempDir(), "umount-invocations.log")
	wrapperScript := filepath.Join(t.TempDir(), "umount-wrapper.sh")
	scriptBody := "#!/bin/sh\necho \"$@\" >> " + umountLog + "\nexec /sbin/umount \"$@\"\n"
	if err := os.WriteFile(wrapperScript, []byte(scriptBody), 0o755); err != nil {
		t.Fatalf("write umount wrapper: %v", err)
	}

	adp := nfsdarwin.New(nfsdarwin.Config{UmountBin: wrapperScript})
	tmp := t.TempDir()
	trunkDir := filepath.Join(tmp, "trunk")
	backingDir := filepath.Join(tmp, "backing")
	runtimeDir := filepath.Join(tmp, "runtime")
	unownedDir := filepath.Join(tmp, "unowned-target")
	for _, d := range []string{trunkDir, backingDir, runtimeDir, unownedDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	sentinel := filepath.Join(unownedDir, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	const viewID = "view-unowned-check"
	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID:     viewID,
		BackingDir: backingDir,
		RuntimeDir: runtimeDir,
		Grants:     []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	realMountPath := prep.PrimaryWorkPath
	t.Cleanup(func() {
		_ = exec.Command("/sbin/umount", "-f", realMountPath).Run()
	})

	if err := adp.DisownMountForTest(viewID, unownedDir); err != nil {
		t.Fatalf("DisownMountForTest: %v", err)
	}
	_ = exec.Command("/sbin/umount", "-f", realMountPath).Run()
	if _, err := adp.Release(t.Context(), viewID); err != nil {
		t.Fatalf("Release with disowned path failed: %v", err)
	}
	if logBytes, err := os.ReadFile(umountLog); err == nil && strings.Contains(string(logBytes), unownedDir) {
		t.Fatalf("Release must never invoke umount on unowned path %s, log:\n%s", unownedDir, string(logBytes))
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "keep\n" {
		t.Fatalf("unowned sentinel disturbed: %q err=%v", string(got), err)
	}
}
