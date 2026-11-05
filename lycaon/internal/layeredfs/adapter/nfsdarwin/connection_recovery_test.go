package nfsdarwin_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/nfsdarwin"
	"github.com/lycaon/lycaon/internal/testutil"
)

const recoveredViewID = "view-recorded"

// startEngineHoldingAMount runs a real engine process with one mounted view and returns the mount
// path, so a test can end that engine the way a crash would.
func startEngineHoldingAMount(t *testing.T, tmp string) (*exec.Cmd, string) {
	t.Helper()
	engine := exec.Command(os.Args[0])
	engine.Env = append(os.Environ(), "NFSDARWIN_ENGINE_PROBE="+tmp)
	engine.Stderr = os.Stderr
	stdout, err := engine.StdoutPipe()
	testutil.FailErr(t, "engine stdout", err)
	stdin, err := engine.StdinPipe()
	testutil.FailErr(t, "engine stdin", err)
	testutil.FailErr(t, "start engine", engine.Start())
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		_ = engine.Process.Kill()
		_ = engine.Wait()
		t.Fatalf("engine did not report a mount: %v", err)
	}
	mountPath := strings.TrimSpace(line)
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = engine.Process.Kill()
		_ = engine.Wait()
		_ = exec.Command(nfsdarwin.DefaultUmountBin, "-f", mountPath).Run()
	})
	return engine, mountPath
}

func runEngineHoldingAMount(tmp string) int {
	trunk := filepath.Join(tmp, "trunk")
	if err := os.MkdirAll(trunk, 0o755); err != nil {
		return 1
	}
	if err := os.WriteFile(filepath.Join(trunk, "f"), []byte("x"), 0o644); err != nil {
		return 1
	}
	adp := nfsdarwin.New(nfsdarwin.Config{})
	prep, err := adp.Prepare(context.Background(), adapter.PrepareRequest{
		ViewID: recoveredViewID, BackingDir: filepath.Join(tmp, "backing"), RuntimeDir: filepath.Join(tmp, "runtime"),
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunk, IsPrimary: true}},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(prep.PrimaryWorkPath)
	_, _ = io.Copy(io.Discard, os.Stdin)
	_, _ = adp.Release(context.Background(), recoveredViewID)
	return 0
}

// TestRecordedConnectionSettlesOnlyByItsIdentity: a later engine process removes the mount of a dead
// one from the durable record alone, leaves a live server's mount and any other mount at the path
// alone, and does not signal a process still holding the settled mount.
func TestRecordedConnectionSettlesOnlyByItsIdentity(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("nfsdarwin requires darwin")
	}
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve temp dir", err)
	engine, mountPath := startEngineHoldingAMount(t, tmp)

	recorded, err := adapter.LoadRecordedConnections(filepath.Join(tmp, "runtime", recoveredViewID))
	testutil.FailErr(t, "load records", err)
	if len(recorded) != 1 {
		t.Fatalf("recorded connections: %+v", recorded)
	}
	rec := recorded[0]
	if rec.AdapterID != adapter.AdapterNFSDarwin || rec.MountPath != mountPath || rec.Server.PID != engine.Process.Pid ||
		!strings.HasPrefix(rec.FSName, "127.0.0.1:/views/") || rec.DevMajorMinor == "" || rec.MountID == "" || rec.ConnectionID == "" {
		t.Fatalf("record does not identify the connection: %+v", rec)
	}

	later := nfsdarwin.New(nfsdarwin.Config{})
	ctx := t.Context()
	if got, err := later.SettleRecordedConnection(ctx, rec); err != nil || got.Disposition != adapter.ConnectionServerAlive {
		t.Fatalf("a live server's mount: %+v err=%v", got, err)
	}
	foreign := rec
	foreign.FSName = "127.0.0.1:/views/other/primary/deadbeef"
	if got, err := later.SettleRecordedConnection(ctx, foreign); err != nil || got.Disposition != adapter.ConnectionForeign {
		t.Fatalf("a different export at the path: %+v err=%v", got, err)
	}
	wrongOwner := rec
	wrongOwner.OwnerUID = rec.OwnerUID + 1
	if got, err := later.SettleRecordedConnection(ctx, wrongOwner); err != nil || got.Disposition != adapter.ConnectionForeign {
		t.Fatalf("another owner's mount: %+v err=%v", got, err)
	}
	if !mountedAt(t, mountPath) {
		t.Fatal("a refused settlement removed the mount")
	}

	holder := exec.Command("/bin/sleep", "600")
	holder.Dir = mountPath
	testutil.FailErr(t, "start holder", holder.Start())
	holderDone := make(chan error, 1)
	go func() { holderDone <- holder.Wait() }()
	t.Cleanup(func() {
		_ = holder.Process.Kill()
		<-holderDone
	})

	testutil.FailErr(t, "kill engine", engine.Process.Kill())
	_ = engine.Wait()
	got, err := later.SettleRecordedConnection(ctx, rec)
	if err != nil || got.Disposition != adapter.ConnectionDetached || !got.Settled() {
		t.Fatalf("a dead server's mount: %+v err=%v", got, err)
	}
	if mountedAt(t, mountPath) {
		t.Fatal("mount remains after settlement")
	}
	select {
	case err := <-holderDone:
		holderDone <- err
		t.Fatalf("settlement ended a process holding the mount: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if got, err := later.SettleRecordedConnection(ctx, rec); err != nil || got.Disposition != adapter.ConnectionAbsent || !got.Settled() {
		t.Fatalf("settling again: %+v err=%v", got, err)
	}
}

func TestReleaseForgetsTheRecordAndAFailedPrepareLeavesNone(t *testing.T) {
	v := prepareRealMount(t, nfsdarwin.Config{}, map[string]string{"f": "x"})
	viewRuntime := filepath.Join(v.runtimeDir, v.id)
	recorded, err := adapter.LoadRecordedConnections(viewRuntime)
	testutil.FailErr(t, "load records", err)
	if len(recorded) != 1 {
		t.Fatalf("records while mounted: %+v", recorded)
	}
	rcpt, err := v.adp.Release(t.Context(), v.id)
	if err != nil || !rcpt.Settled {
		t.Fatalf("release: %+v err=%v", rcpt, err)
	}
	if recorded, err := adapter.LoadRecordedConnections(viewRuntime); err != nil || len(recorded) != 0 {
		t.Fatalf("records after release: %+v err=%v", recorded, err)
	}

	failing := nfsdarwin.New(nfsdarwin.Config{MountNFSBin: "/usr/bin/false"})
	tmp := t.TempDir()
	runtimeDir := filepath.Join(tmp, "runtime")
	_, err = failing.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: "view-failed", BackingDir: filepath.Join(tmp, "backing"), RuntimeDir: runtimeDir,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: v.trunk, IsPrimary: true}},
	})
	if err == nil {
		t.Fatal("expected Prepare to fail with a failing mount helper")
	}
	if recorded, err := adapter.LoadRecordedConnections(filepath.Join(runtimeDir, "view-failed")); err != nil || len(recorded) != 0 {
		t.Fatalf("records after failed prepare: %+v err=%v", recorded, err)
	}
}

func mountedAt(t *testing.T, path string) bool {
	t.Helper()
	out, err := exec.Command("/sbin/mount").Output()
	testutil.FailErr(t, "read mount table", err)
	return strings.Contains(string(out), " on "+path+" (")
}

// TestMountHelperThatAttachedAndThenFailedLeavesNoMount: a helper can fail or be killed after the kernel
// attached the mount, and the server closes on that failure, so what it left would be a dead mount.
func TestMountHelperThatAttachedAndThenFailedLeavesNoMount(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("nfsdarwin requires darwin")
	}
	tmp := t.TempDir()
	trunk, helper := filepath.Join(tmp, "trunk"), filepath.Join(tmp, "attach-then-fail")
	testutil.FailErr(t, "create trunk", os.MkdirAll(trunk, 0o755))
	script := "#!/bin/sh\n" + nfsdarwin.DefaultMountNFSBin + " \"$@\"\nexit 1\n"
	testutil.FailErr(t, "write helper", os.WriteFile(helper, []byte(script), 0o755))
	runtimeDir := filepath.Join(tmp, "runtime")
	mountPath := filepath.Join(runtimeDir, "view-attached", "mounts", "primary")
	t.Cleanup(func() { _ = exec.Command(nfsdarwin.DefaultUmountBin, "-f", mountPath).Run() })

	_, err := nfsdarwin.New(nfsdarwin.Config{MountNFSBin: helper}).Prepare(t.Context(), adapter.PrepareRequest{
		ViewID: "view-attached", BackingDir: filepath.Join(tmp, "backing"), RuntimeDir: runtimeDir,
		Grants: []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunk, IsPrimary: true}},
	})
	if err == nil {
		t.Fatal("expected Prepare to report the failed helper")
	}
	// Rollback keeps the records while anything is still mounted below the view.
	recorded, err := adapter.LoadRecordedConnections(filepath.Join(runtimeDir, "view-attached"))
	if err != nil || len(recorded) != 0 {
		t.Fatalf("records after failed prepare: %+v err=%v", recorded, err)
	}
}
