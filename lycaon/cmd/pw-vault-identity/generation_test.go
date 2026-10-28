package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/launchverify"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/vaultidentity"
)

func TestServicePreservesCustodyForRunningGenerationAcrossPackageUpgrade(t *testing.T) {
	f := newFixture(t)
	shaA := f.activeGeneration
	lockA := f.lockGeneration(shaA)
	client := f.start()
	const account = "age-x25519-identity-v1:0a"
	if resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpCreate, Account: account, Value: []byte("kept-across-upgrade")}); resp.Status != vaultidentity.StatusOK {
		t.Fatalf("create on generation A = %+v", resp)
	}
	testutil.FailErr(t, "close initial client", client.Close())

	// In-place upgrade: stage generation B, switch launch.json to B, and
	// overwrite top-level manifest.json with B.
	desktopB := []byte("desktop app generation B")
	engineB := []byte("engine generation B")
	shaB := f.stageGeneration("1.0.3", desktopB, engineB)
	f.activateGeneration(shaB)
	testutil.FailErr(t, "write top-level manifest B", os.WriteFile(filepath.Join(f.base, "manifest.json"), marshalManifest(t, "1.0.3", entry(launchverify.DesktopAppPath, desktopB), entry(launchverify.EnginePath, engineB)), 0o644))

	// Session A still holds LOCK_SH on shaA.lock and keeps custody for status
	// probes, reads, and sidecar restarts inside generation A.
	if resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpRead, Account: account}); resp.Status != vaultidentity.StatusOK || string(resp.Value) != "kept-across-upgrade" || resp.LauncherPID != 300 {
		t.Fatalf("read on running generation A after upgrade = %+v", resp)
	}
	f.addProcess(process{pid: 302, ppid: 300, uid: 1000, start: 25, image: engineImage, generation: shaA})
	testutil.FailErr(t, "close before sidecar restart", client.Close())
	f.peer = Peer{UID: 1000, PID: 302}
	if resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpRead, Account: account}); resp.Status != vaultidentity.StatusOK || string(resp.Value) != "kept-across-upgrade" || resp.LauncherPID != 300 {
		t.Fatalf("read after sidecar relaunch on generation A = %+v", resp)
	}

	// A new launch uses generation B and accesses the same user's custody.
	f.addProcess(process{pid: 400, ppid: 1, uid: 1000, start: 30, image: desktopB, exePath: filepath.Join(f.generationsDir, shaB, "bin/painted-wolf-code")})
	f.addProcess(process{pid: 401, ppid: 400, uid: 1000, start: 40, image: engineB, exePath: filepath.Join(f.generationsDir, shaB, "bin/pw")})
	testutil.FailErr(t, "close before launch B", client.Close())
	f.peer = Peer{UID: 1000, PID: 401}
	if resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpRead, Account: account}); resp.Status != vaultidentity.StatusOK || string(resp.Value) != "kept-across-upgrade" || resp.LauncherPID != 400 {
		t.Fatalf("read on new launch B = %+v", resp)
	}

	// Once session A exits and releases its generation lock, generation A is refused.
	testutil.FailErr(t, "release generation A lock", lockA.Close())
	testutil.FailErr(t, "close before retired check", client.Close())
	f.peer = Peer{UID: 1000, PID: 302}
	if resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpRead, Account: account}); resp.Status != vaultidentity.StatusRefused || resp.Code != string(launchverify.CodeExecutableMismatch) {
		t.Fatalf("retired unlocked generation A = %+v", resp)
	}
}
