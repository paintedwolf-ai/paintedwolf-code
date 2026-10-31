package modelstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func timeAfterMs(ms int) <-chan time.Time {
	return time.After(time.Duration(ms) * time.Millisecond)
}

type acceptSigner struct{ fail bool }

func (a acceptSigner) Verify(SignedManifest) error {
	if a.fail {
		return errors.New("signature rejected")
	}
	return nil
}

func testArtifact(t *testing.T, payload []byte) Artifact {
	t.Helper()
	sum := sha256.Sum256(payload)
	return Artifact{Digest: hex.EncodeToString(sum[:]), Format: "safetensors", Bytes: int64(len(payload))}
}

func newStore(t *testing.T) *Store {
	t.Helper()
	return New(filepath.Join(t.TempDir(), "models"), acceptSigner{})
}

func TestStageVerifyActivate(t *testing.T) {
	s := newStore(t)
	payload := []byte("weights-bytes")
	a := testArtifact(t, payload)

	if err := s.Stage(newReader(payload), a); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if !s.Verified(a.Digest) {
		t.Fatal("staged artifact should verify")
	}

	m := Manifest{Model: "gemma4-e4b-paintedwolf", Revision: "proto-1", Device: "cpu", Artifact: a}
	if err := s.Activate(SignedManifest{Manifest: m}); err != nil {
		t.Fatalf("activate: %v", err)
	}
	active, err := s.Active()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if got := active["gemma4-e4b-paintedwolf/cpu"]; got.Revision != "proto-1" {
		t.Fatalf("revision: got %q", got.Revision)
	}
}

func TestDigestMismatchLeavesNothing(t *testing.T) {
	s := newStore(t)
	a := testArtifact(t, []byte("real"))
	if err := s.Stage(newReader([]byte("wrong")), a); err == nil {
		t.Fatal("expected digest mismatch error")
	}
	if s.Verified(a.Digest) {
		t.Fatal("mismatched payload must not verify")
	}
}

func TestOversizeInputRejected(t *testing.T) {
	s := newStore(t)
	a := testArtifact(t, []byte("real"))
	a.Bytes = 1
	if err := s.Stage(newReader([]byte("too long")), a); err == nil {
		t.Fatal("expected size mismatch error")
	}
}

func TestBadIdentityRejected(t *testing.T) {
	s := newStore(t)
	for _, digest := range []string{"", "x", "../escape", "a\nb"} {
		a := Artifact{Digest: digest, Bytes: 4}
		if err := s.Stage(newReader([]byte("data")), a); err == nil {
			t.Fatalf("digest %q: expected rejection", digest)
		}
	}
	m := Manifest{Model: "../escape", Revision: "r", Device: "cpu", Artifact: Artifact{Digest: "missing", Bytes: 1}}
	if err := s.Activate(SignedManifest{Manifest: m}); err == nil {
		t.Fatal("expected traversal identity rejection")
	}
}

func TestDuplicateArtifactEntryRejected(t *testing.T) {
	s := newStore(t)
	payload := []byte("weights")
	a := testArtifact(t, payload)
	if err := s.Stage(newReader(payload), a); err != nil {
		t.Fatalf("stage: %v", err)
	}
	m := Manifest{
		Model:    "m",
		Revision: "r",
		Device:   "cpu",
		Artifact: a,
		Files:    []Artifact{a},
	}
	if err := s.Activate(SignedManifest{Manifest: m}); err == nil {
		t.Fatal("expected duplicate artifact entry rejection")
	}
}

func TestActivateRequiresSignedManifest(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "models"), acceptSigner{fail: true})
	payload := []byte("weights")
	a := testArtifact(t, payload)
	if err := s.Stage(newReader(payload), a); err != nil {
		t.Fatalf("stage: %v", err)
	}
	m := Manifest{Model: "m", Revision: "r", Device: "cpu", Artifact: a}
	if err := s.Activate(SignedManifest{Manifest: m}); err == nil {
		t.Fatal("expected unsigned manifest rejection")
	}
}

func TestActivateRequiresVerifiedArtifact(t *testing.T) {
	s := newStore(t)
	m := Manifest{Model: "m", Revision: "r", Device: "cpu", Artifact: Artifact{Digest: missingDigest(), Bytes: 1}}
	if err := s.Activate(SignedManifest{Manifest: m}); err == nil {
		t.Fatal("expected activation of unverified artifact to fail")
	}
}

func missingDigest() string {
	return hex.EncodeToString(make([]byte, 32))
}

func TestNewestJournalEntryWins(t *testing.T) {
	s := newStore(t)
	payload := []byte("v2")
	a := testArtifact(t, payload)
	if err := s.Stage(newReader(payload), a); err != nil {
		t.Fatalf("stage: %v", err)
	}
	for _, rev := range []string{"r1", "r2"} {
		if err := s.Activate(SignedManifest{Manifest: Manifest{Model: "m", Revision: rev, Device: "cpu", Artifact: a}}); err != nil {
			t.Fatalf("activate %s: %v", rev, err)
		}
	}
	active, err := s.Active()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if got := active["m/cpu"].Revision; got != "r2" {
		t.Fatalf("newest revision: got %q want r2", got)
	}
}

func TestTornTailRecoversAndNextActivateSurvives(t *testing.T) {
	s := newStore(t)
	payload := []byte("weights")
	a := testArtifact(t, payload)
	if err := s.Stage(newReader(payload), a); err != nil {
		t.Fatalf("stage: %v", err)
	}
	m := Manifest{Model: "m", Revision: "r1", Device: "cpu", Artifact: a}
	if err := s.Activate(SignedManifest{Manifest: m}); err != nil {
		t.Fatalf("activate: %v", err)
	}
	f, err := os.OpenFile(s.journalPath(), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, _ = f.Write([]byte(`{"model":"m","revi`))
	_ = f.Close()

	m2 := Manifest{Model: "m", Revision: "r2", Device: "cpu", Artifact: a}
	if err := s.Activate(SignedManifest{Manifest: m2}); err != nil {
		t.Fatalf("activate after tear: %v", err)
	}
	active, err := s.Active()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if got := active["m/cpu"].Revision; got != "r2" {
		t.Fatalf("revision after recovery: got %q want r2", got)
	}
}

func TestCorruptMiddleIsAnError(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		t.Fatalf("root: %v", err)
	}
	if err := os.WriteFile(s.journalPath(), []byte("not json\n"), 0o644); err != nil {
		t.Fatalf("seed journal: %v", err)
	}
	m := Manifest{Model: "m", Revision: "r", Device: "cpu", Artifact: Artifact{Digest: missingDigest(), Bytes: 1}}
	if err := s.Activate(SignedManifest{Manifest: m}); err == nil {
		t.Fatal("expected corruption before the tail to fail activation")
	}
}

func newReader(b []byte) *os.File {
	f, err := os.CreateTemp("", "payload")
	if err != nil {
		panic(err)
	}
	_, _ = f.Write(b)
	_, _ = f.Seek(0, 0)
	return f
}

func TestSymlinkEscapeRejected(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		t.Fatalf("root: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(s.root, "artifacts")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	payload := []byte("weights-bytes")
	a := testArtifact(t, payload)
	if err := s.Stage(newReader(payload), a); err == nil {
		t.Fatal("expected symlinked artifacts dir to fail staging")
	}
	if _, err := os.Lstat(filepath.Join(outside, a.Digest[:2])); !os.IsNotExist(err) {
		t.Fatal("staging escaped the store")
	}
	if s.Verified(a.Digest) {
		t.Fatal("verified followed a symlink out of the store")
	}
}

func TestCorruptCompleteRecordFailsActive(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		t.Fatalf("root: %v", err)
	}
	payload := []byte("weights-bytes")
	a := testArtifact(t, payload)
	if err := s.Stage(newReader(payload), a); err != nil {
		t.Fatalf("stage: %v", err)
	}
	m := Manifest{Model: "m", Revision: "r", Device: "cpu", Artifact: a}
	if err := s.Activate(SignedManifest{Manifest: m}); err != nil {
		t.Fatalf("activate: %v", err)
	}
	raw, err := os.ReadFile(s.journalPath())
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if err := os.WriteFile(s.journalPath(), append([]byte("garbage"), raw...), 0o644); err != nil {
		t.Fatalf("corrupt journal: %v", err)
	}
	if _, err := s.Active(); err == nil {
		t.Fatal("expected Active to reject a corrupt complete record")
	}
	if err := s.Activate(SignedManifest{Manifest: Manifest{Model: "m", Revision: "r2", Device: "cpu", Artifact: a}}); err == nil {
		t.Fatal("expected Activate to reject a corrupt complete record")
	}
}

func TestCollectRetainsActivePreviousAndLeasedRevisions(t *testing.T) {
	s := newStore(t)
	a1 := testArtifact(t, []byte("rev-1"))
	a2 := testArtifact(t, []byte("rev-2"))
	a3 := testArtifact(t, []byte("rev-3"))
	unreferenced := testArtifact(t, []byte("unreferenced"))

	for _, item := range []struct {
		art  Artifact
		data []byte
	}{
		{a1, []byte("rev-1")},
		{a2, []byte("rev-2")},
		{a3, []byte("rev-3")},
		{unreferenced, []byte("unreferenced")},
	} {
		if err := s.Stage(newReader(item.data), item.art); err != nil {
			t.Fatalf("stage %s: %v", item.art.Digest, err)
		}
	}

	if err := s.Activate(SignedManifest{Manifest: Manifest{Model: "gemma4-e4b", Revision: "r1", Device: "mlx", Artifact: a1}}); err != nil {
		t.Fatalf("activate r1: %v", err)
	}
	leaseR1, err := s.AcquireLease("gemma4-e4b", "mlx", "nativegen", 1)
	if err != nil {
		t.Fatalf("lease r1: %v", err)
	}

	if err := s.Activate(SignedManifest{Manifest: Manifest{Model: "gemma4-e4b", Revision: "r2", Device: "mlx", Artifact: a2}}); err != nil {
		t.Fatalf("activate r2: %v", err)
	}
	if err := s.Activate(SignedManifest{Manifest: Manifest{Model: "gemma4-e4b", Revision: "r3", Device: "mlx", Artifact: a3}}); err != nil {
		t.Fatalf("activate r3: %v", err)
	}

	// While r1 is leased, Collect removes only the unreferenced artifact; r3 (active), r2 (previous), and r1 (leased) stay.
	removed, err := s.Collect()
	if err != nil {
		t.Fatalf("collect with lease: %v", err)
	}
	if len(removed) != 1 || removed[0] != unreferenced.Digest {
		t.Fatalf("collect with lease removed %v, want [%s]", removed, unreferenced.Digest)
	}
	for _, kept := range []Artifact{a1, a2, a3} {
		if !s.Verified(kept.Digest) {
			t.Fatalf("expected %s to remain verified while leased/retained", kept.Digest)
		}
	}

	// Once the r1 lease ends, Collect removes r1 while keeping r3 (active) and r2 (previous rollback revision).
	leaseR1.Release()
	removed, err = s.Collect()
	if err != nil {
		t.Fatalf("collect after release: %v", err)
	}
	if len(removed) != 1 || removed[0] != a1.Digest {
		t.Fatalf("collect after release removed %v, want [%s]", removed, a1.Digest)
	}
	if s.Verified(a1.Digest) {
		t.Fatal("expected r1 artifact to be collected after lease release")
	}
	if !s.Verified(a2.Digest) || !s.Verified(a3.Digest) {
		t.Fatal("expected active (r3) and previous (r2) artifacts to remain verified")
	}
}

func TestInsufficientDiskSpacePreservesWorkingModel(t *testing.T) {
	s := newStore(t)
	workingPayload := []byte("working-weights")
	working := testArtifact(t, workingPayload)
	if err := s.Stage(newReader(workingPayload), working); err != nil {
		t.Fatalf("stage working: %v", err)
	}
	if err := s.Activate(SignedManifest{Manifest: Manifest{Model: "gemma4-e4b", Revision: "r1", Device: "mlx", Artifact: working}}); err != nil {
		t.Fatalf("activate working: %v", err)
	}

	s.SetFreeBytesFunc(func(string) (int64, error) {
		return 8, nil
	})
	nextPayload := []byte("new-weights-larger-than-free-bytes")
	next := testArtifact(t, nextPayload)
	if err := s.Stage(newReader(nextPayload), next); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("expected ErrInsufficientSpace, got %v", err)
	}
	if s.Verified(next.Digest) {
		t.Fatal("expected rejected staging to leave no verified artifact")
	}
	status := s.Status("gemma4-e4b", "mlx")
	if status.State != StateReady || status.Active == nil || status.Active.Revision != "r1" {
		t.Fatalf("working model altered after failed space admission: %+v", status)
	}
}

func TestReadinessAndRepairStates(t *testing.T) {
	s := newStore(t)
	if st := s.Status("gemma4-e4b", "mlx"); st.State != StateAbsent || st.Repair != RepairImportSeed {
		t.Fatalf("absent status: %+v", st)
	}

	payload := []byte("weights")
	a := testArtifact(t, payload)
	if err := s.Stage(newReader(payload), a); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := s.Activate(SignedManifest{Manifest: Manifest{Model: "gemma4-e4b", Revision: "r1", Device: "mlx", Artifact: a}}); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if st := s.Status("gemma4-e4b", "mlx"); st.State != StateReady || st.Repair != RepairNone {
		t.Fatalf("ready status: %+v", st)
	}

	// Corrupt the backing artifact on disk to verify repair detection.
	artPath := filepath.Join(s.root, s.artifactRel(a.Digest))
	if err := os.WriteFile(artPath, []byte("corrupted"), 0o644); err != nil {
		t.Fatalf("corrupt artifact: %v", err)
	}
	if st := s.Status("gemma4-e4b", "mlx"); st.State != StateRepairRequired || st.Repair != RepairRestageArtifact {
		t.Fatalf("corrupt artifact status: %+v", st)
	}
}

func TestCollectReleasesSupplementaryLeasePins(t *testing.T) {
	s := newStore(t)
	mainR1 := testArtifact(t, []byte("main-r1"))
	suppR1 := testArtifact(t, []byte("tokenizer-r1"))
	suppR1.Format = "tokenizer.json"
	mainR2 := testArtifact(t, []byte("main-r2"))
	mainR3 := testArtifact(t, []byte("main-r3"))

	for _, item := range []struct {
		art  Artifact
		data []byte
	}{
		{mainR1, []byte("main-r1")},
		{suppR1, []byte("tokenizer-r1")},
		{mainR2, []byte("main-r2")},
		{mainR3, []byte("main-r3")},
	} {
		if err := s.Stage(newReader(item.data), item.art); err != nil {
			t.Fatalf("stage %s: %v", item.art.Digest, err)
		}
	}

	if err := s.Activate(SignedManifest{Manifest: Manifest{
		Model:    "gemma4-e4b",
		Revision: "r1",
		Device:   "cpu",
		Artifact: mainR1,
		Files:    []Artifact{suppR1},
	}}); err != nil {
		t.Fatalf("activate r1: %v", err)
	}
	lease, err := s.AcquireLease("gemma4-e4b", "cpu", "nativegen", 1)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lease.CheckpointDir(), "model.safetensors")); err != nil {
		t.Fatalf("materialized model.safetensors: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lease.CheckpointDir(), "tokenizer.json")); err != nil {
		t.Fatalf("materialized tokenizer.json: %v", err)
	}

	for _, item := range []struct {
		rev string
		art Artifact
	}{
		{"r2", mainR2},
		{"r3", mainR3},
	} {
		if err := s.Activate(SignedManifest{Manifest: Manifest{
			Model:    "gemma4-e4b",
			Revision: item.rev,
			Device:   "cpu",
			Artifact: item.art,
		}}); err != nil {
			t.Fatalf("activate %s: %v", item.rev, err)
		}
	}

	lease.Release()
	lease.Release()

	removed, err := s.Collect()
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed = %v, want 2 digests (mainR1 and suppR1)", removed)
	}
	if s.Verified(mainR1.Digest) {
		t.Fatal("retired primary artifact remained verified after lease release and collect")
	}
	if s.Verified(suppR1.Digest) {
		t.Fatal("retired supplementary artifact remained verified after lease release and collect")
	}
}

type callbackSigner struct {
	onVerify func() error
}

func (c callbackSigner) Verify(SignedManifest) error {
	if c.onVerify != nil {
		return c.onVerify()
	}
	return nil
}

func TestActivateHoldsJournalLockAcrossVerificationAndPublication(t *testing.T) {
	verifiedSig := make(chan struct{})
	s := New(filepath.Join(t.TempDir(), "models"), callbackSigner{
		onVerify: func() error {
			close(verifiedSig)
			return nil
		},
	})
	payload := []byte("weights-under-concurrent-collect")
	art := testArtifact(t, payload)
	if err := s.Stage(newReader(payload), art); err != nil {
		t.Fatalf("stage: %v", err)
	}

	lock, err := s.lockJournal()
	if err != nil {
		t.Fatalf("lockJournal: %v", err)
	}

	activateErrCh := make(chan error, 1)
	go func() {
		activateErrCh <- s.Activate(SignedManifest{Manifest: Manifest{
			Model:    "gemma4-e4b",
			Revision: "r1",
			Device:   "cpu",
			Artifact: art,
		}})
	}()

	<-verifiedSig
	// Give Activate a brief window to reach lockJournal (or, if unlocked, run Verified prematurely).
	select {
	case err := <-activateErrCh:
		lock.Unlock()
		t.Fatalf("Activate completed while journal lock was held: %v", err)
	case <-timeAfterMs(25):
	}

	root, err := s.openRoot()
	if err != nil {
		lock.Unlock()
		t.Fatalf("openRoot: %v", err)
	}
	if err := root.Remove(s.artifactRel(art.Digest)); err != nil {
		root.Close()
		lock.Unlock()
		t.Fatalf("remove staged artifact under lock: %v", err)
	}
	root.Close()
	lock.Unlock()

	if err := <-activateErrCh; err == nil {
		t.Fatal("expected Activate to fail when artifact was collected before journal lock acquisition")
	}
	if st := s.Status("gemma4-e4b", "cpu"); st.State != StateAbsent {
		t.Fatalf("status after rejected activation = %+v, want StateAbsent", st)
	}
}
