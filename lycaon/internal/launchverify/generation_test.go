package launchverify

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestVerifyChainAcceptsPeerOnRetainedLockedGenerationAfterUpgrade(t *testing.T) {
	proc, verifier := newFixture(t)
	shaA := proc.activeGeneration
	lockA := proc.lockGeneration(shaA)
	proc.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, start: 10, image: desktopImage, generation: shaA})
	proc.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, start: 20, image: engineImage, generation: shaA})

	// Simulate an in-place package upgrade to generation B: stage B, update
	// launch.json to B, and overwrite a top-level manifest.json with B.
	desktopB := []byte("desktop app generation B bytes")
	engineB := []byte("engine generation B bytes")
	shaB, manifestB := proc.stageGeneration("1.0.1", desktopB, engineB)
	proc.activateGeneration(shaB)
	rawB, err := json.Marshal(manifestB)
	testutil.FailErr(t, "marshal manifest B", err)
	testutil.FailErr(t, "write top-level manifest B", os.WriteFile(filepath.Join(proc.base, "manifest.json"), rawB, 0o644))

	// Session A (holding LOCK_SH on shaA.lock) still verifies against generation A.
	chainA, err := verifier.VerifyChain(301, 1000)
	testutil.FailErr(t, "verify chain on retained generation A", err)
	if chainA.Generation != shaA {
		t.Fatalf("chainA.Generation = %q, want %q", chainA.Generation, shaA)
	}
	testutil.FailErr(t, "recheck chain on retained generation A", verifier.Recheck(chainA))

	// A new launch on generation B verifies against generation B.
	proc.add(fakeProcess{pid: 400, ppid: 1, uid: 1000, euid: 1000, start: 30, image: desktopB, exePath: filepath.Join(proc.generationsDir, shaB, "bin/painted-wolf-code")})
	proc.add(fakeProcess{pid: 401, ppid: 400, uid: 1000, euid: 1000, start: 40, image: engineB, exePath: filepath.Join(proc.generationsDir, shaB, "bin/pw")})
	chainB, err := verifier.VerifyChain(401, 1000)
	testutil.FailErr(t, "verify chain on active generation B", err)
	if chainB.Generation != shaB {
		t.Fatalf("chainB.Generation = %q, want %q", chainB.Generation, shaB)
	}

	// Once session A exits and releases its lifetime lock, generation A is no longer accepted.
	testutil.FailErr(t, "release generation A lock", lockA.Close())
	if err := verifier.Recheck(chainA); CodeOf(err) != CodeExecutableMismatch {
		t.Fatalf("recheck after lock release = %v, want executable_mismatch", err)
	}
	if _, err := verifier.VerifyChain(301, 1000); CodeOf(err) != CodeExecutableMismatch {
		t.Fatalf("verify after lock release = %v, want executable_mismatch", err)
	}
}

func TestVerifyChainRefusesWhenPeerGenerationCannotBeEstablished(t *testing.T) {
	t.Run("unlocked retired generation", func(t *testing.T) {
		proc, verifier := newFixture(t)
		shaA := proc.activeGeneration
		proc.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, start: 10, image: desktopImage, generation: shaA})
		proc.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, start: 20, image: engineImage, generation: shaA})
		shaB, _ := proc.stageGeneration("1.0.1", []byte("desktop B"), []byte("engine B"))
		proc.activateGeneration(shaB)
		if _, err := verifier.VerifyChain(301, 1000); CodeOf(err) != CodeExecutableMismatch {
			t.Fatalf("unlocked retired generation = %v, want executable_mismatch", err)
		}
	})
	t.Run("unlinked or copied binary outside generation", func(t *testing.T) {
		proc, verifier := newFixture(t)
		copiedEngine := filepath.Join(proc.base, "copied-pw")
		testutil.FailErr(t, "write copied engine", os.WriteFile(copiedEngine, engineImage, 0o755))
		proc.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, start: 10, image: desktopImage})
		proc.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, start: 20, image: engineImage, exePath: copiedEngine})
		if _, err := verifier.VerifyChain(301, 1000); CodeOf(err) != CodeExecutableMismatch {
			t.Fatalf("copied engine outside generation = %v, want executable_mismatch", err)
		}
	})
	t.Run("cross-generation parent and child", func(t *testing.T) {
		proc, verifier := newFixture(t)
		shaA := proc.activeGeneration
		_ = proc.lockGeneration(shaA)
		desktopB := []byte("desktop B")
		engineB := []byte("engine B")
		shaB, _ := proc.stageGeneration("1.0.1", desktopB, engineB)
		proc.activateGeneration(shaB)
		proc.add(fakeProcess{pid: 300, ppid: 1, uid: 1000, euid: 1000, start: 10, image: desktopB, exePath: filepath.Join(proc.generationsDir, shaB, "bin/painted-wolf-code")})
		proc.add(fakeProcess{pid: 301, ppid: 300, uid: 1000, euid: 1000, start: 20, image: engineImage, generation: shaA})
		_, err := verifier.VerifyChain(301, 1000)
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeExecutableMismatch || refusal.Role != RoleDesktopApp {
			t.Fatalf("cross-generation chain = %v, want executable_mismatch for %s", err, RoleDesktopApp)
		}
	})
}

func TestGenerationPolicyAndLockValidation(t *testing.T) {
	var zero Verifier
	layout := zero.layout()
	if layout.procRoot != "/proc" || layout.generationsDir != DefaultGenerationsDir || layout.policyPath != DefaultPolicyPath || layout.locksDir != DefaultLocksDir || layout.anchor != "/" {
		t.Fatal("default verifier paths differ from installed layout")
	}
	if rolePath(nil, "unknown") != "" {
		t.Fatal("unknown role resolved a path")
	}

	proc, verifier := newFixture(t)
	sha := proc.activeGeneration
	// Invalid policy shapes are rejected.
	for name, content := range map[string]string{
		"trailing":      `{"version":1,"generation_root":"` + filepath.Join(proc.generationsDir, sha) + `","manifest_sha256":"` + sha + `","logical_install_root":"/opt/painted-wolf-code"}{}`,
		"bad version":   `{"version":2,"generation_root":"` + filepath.Join(proc.generationsDir, sha) + `","manifest_sha256":"` + sha + `","logical_install_root":"/opt/painted-wolf-code"}`,
		"outside root":  `{"version":1,"generation_root":"/tmp/` + sha + `","manifest_sha256":"` + sha + `","logical_install_root":"/opt/painted-wolf-code"}`,
		"bad logical":   `{"version":1,"generation_root":"` + filepath.Join(proc.generationsDir, sha) + `","manifest_sha256":"` + sha + `","logical_install_root":"/"}`,
		"mismatch root": `{"version":1,"generation_root":"` + filepath.Join(proc.generationsDir, sha) + `","manifest_sha256":"` + sha + `","logical_install_root":"/opt/other"}`,
		"oversized":     strings.Repeat(" ", maxPolicyBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			testutil.FailErr(t, "write policy", os.WriteFile(proc.policyPath, []byte(content), 0o644))
			if _, err := verifier.layout().resolve(301, 300); CodeOf(err) != CodeManifestUnreadable {
				t.Fatalf("%s: err = %v, want manifest_unreadable", name, err)
			}
		})
	}
	proc.activateGeneration(sha)

	// Tampered manifest content or permissions inside the generation are rejected.
	manifestFile := filepath.Join(proc.generationsDir, sha, "manifest.json")
	origManifest, err := os.ReadFile(manifestFile)
	testutil.FailErr(t, "read manifest", err)
	testutil.FailErr(t, "tamper manifest", os.WriteFile(manifestFile, append(origManifest, ' '), 0o644))
	if _, err := verifier.layout().loadGeneration(sha); err == nil {
		t.Fatal("accepted manifest whose bytes differ from generation digest")
	}
	testutil.FailErr(t, "restore manifest", os.WriteFile(manifestFile, origManifest, 0o644))
	testutil.FailErr(t, "writable manifest", os.Chmod(manifestFile, 0o666))
	if _, err := verifier.layout().loadGeneration(sha); err == nil {
		t.Fatal("accepted world-writable manifest")
	}
	testutil.FailErr(t, "restore manifest mode", os.Chmod(manifestFile, 0o644))

	// Lock file permissions and procfs flock fallback.
	if verifier.layout().generationLocked("not-a-digest", 300) {
		t.Fatal("accepted invalid digest lock")
	}
	lock := proc.lockGeneration(sha)
	testutil.FailErr(t, "loosen lock mode", os.Chmod(filepath.Join(proc.locksDir, sha+".lock"), 0o644))
	if verifier.layout().generationLocked(sha, 300) {
		t.Fatal("accepted 0644 generation lock")
	}
	testutil.FailErr(t, "restore lock mode", os.Chmod(filepath.Join(proc.locksDir, sha+".lock"), 0o600))
	_ = lock.Close()

	targetLock := fileFacts{device: 0xfd01, inode: 4242}
	if sharedFlockHeld(nil, proc.root, 300, targetLock) {
		t.Fatal("procHasSharedFlock matched without lock records")
	}
	fdinfoDir := filepath.Join(proc.root, "300", "fdinfo")
	testutil.FailErr(t, "mkdir fdinfo", os.MkdirAll(fdinfoDir, 0o755))
	testutil.FailErr(t, "write fdinfo", os.WriteFile(filepath.Join(fdinfoDir, "7"), []byte("pos:\t0\nlock:\t1: FLOCK  ADVISORY  READ 300 fd:01:4242 0 EOF\n"), 0o644))
	if !sharedFlockHeld(nil, proc.root, 300, targetLock) {
		t.Fatal("procHasSharedFlock did not recognize fdinfo shared flock")
	}
	testutil.FailErr(t, "remove fdinfo", os.RemoveAll(fdinfoDir))
	testutil.FailErr(t, "write proc locks", os.WriteFile(filepath.Join(proc.root, "locks"), []byte("1: POSIX  ADVISORY  WRITE 1 fd:01:4242 0 EOF\n2: FLOCK  ADVISORY  READ 300 fd:01:4242 0 EOF\n"), 0o644))
	if !sharedFlockHeld(nil, proc.root, 300, targetLock) {
		t.Fatal("procHasSharedFlock did not recognize /proc/locks shared flock")
	}
}
