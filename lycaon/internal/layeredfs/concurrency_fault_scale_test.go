package layeredfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// TestCopyUpSourceChangedInPlaceAndAtomicReplacement proves that when the lower source changes
// during copy-up capture (either by in-place write — including adversarial mid-read byte mutation
// that restores timestamps/size/inode — or by atomic replacement), the core either retries to a
// coherent capture where base == initial private bytes, or fails with ErrSourceChangedDuringCapture
// before publishing any mutation.
func TestCopyUpSourceChangedInPlaceAndAtomicReplacement(t *testing.T) {
	t.Parallel()

	t.Run("adversarial_mid_read_inplace_mutation_with_timestamp_restoration_refuses_when_persistent", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		trunk := filepath.Join(tmp, "trunk")
		backing := filepath.Join(tmp, "backing")
		testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
		testutil.FailErr(t, "mkdir backing", os.MkdirAll(backing, 0o700))

		srcFile := filepath.Join(trunk, "race.txt")
		initialBytes := bytes.Repeat([]byte("AAAA"), 1024)
		fixedMtime := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
		testutil.FailErr(t, "write race.txt", os.WriteFile(srcFile, initialBytes, 0o644))
		testutil.FailErr(t, "chtimes race.txt", os.Chtimes(srcFile, fixedMtime, fixedMtime))

		// Every attempt mutates the second half of the file in-place mid-read while preserving size and mtime!
		core, err := New(Config{
			ViewID:            "view-inplace-fail",
			BackingDir:        backing,
			MaxCaptureRetries: 3,
			Roots: []RootSpec{
				{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true},
			},
			CaptureRetryHook: func(_ QualifiedPath, attempt int) {
				mutated := append(bytes.Repeat([]byte("AAAA"), 512), bytes.Repeat([]byte(fmt.Sprintf("%d%d%d%d", attempt, attempt, attempt, attempt)), 512)...)
				_ = os.WriteFile(srcFile, mutated, 0o644)
				_ = os.Chtimes(srcFile, fixedMtime, fixedMtime)
			},
		})
		testutil.FailErr(t, "New Core", err)
		defer func() { _ = core.ReleaseView() }()

		qp := QualifiedPath{RootID: "primary", Rel: "race.txt"}
		_, err = core.WriteFile(qp, []byte("worker-mutation"), 0o644)
		if !errors.Is(err, ErrSourceChangedDuringCapture) {
			t.Fatalf("expected ErrSourceChangedDuringCapture on persistent mid-read mutation, got %v", err)
		}

		// Verify no mutation or partial base was published.
		if _, ok := core.CapturedBase(qp); ok {
			t.Fatal("expected no captured base to be published after failed capture")
		}
		changes, err := core.EnumerateChanges(false)
		testutil.FailErr(t, "EnumerateChanges", err)
		if len(changes) != 0 {
			t.Fatalf("expected 0 changes after refused copy-up, got %+v", changes)
		}
	})

	t.Run("atomic_replacement_during_first_attempt_recovers_coherent_base_on_retry", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		trunk := filepath.Join(tmp, "trunk")
		backing := filepath.Join(tmp, "backing")
		testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
		testutil.FailErr(t, "mkdir backing", os.MkdirAll(backing, 0o700))

		srcFile := filepath.Join(trunk, "atomic.txt")
		v1Bytes := []byte("version-1-bytes-00000000")
		v2Bytes := []byte("version-2-bytes-11111111")
		testutil.FailErr(t, "write atomic.txt v1", os.WriteFile(srcFile, v1Bytes, 0o644))

		core, err := New(Config{
			ViewID:            "view-atomic-retry",
			BackingDir:        backing,
			MaxCaptureRetries: 4,
			Roots: []RootSpec{
				{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true},
			},
			CaptureRetryHook: func(_ QualifiedPath, attempt int) {
				if attempt == 0 {
					// Atomically replace srcFile on attempt 0 mid-capture.
					tmpFile := filepath.Join(trunk, ".atomic.tmp")
					_ = os.WriteFile(tmpFile, v2Bytes, 0o644)
					_ = os.Rename(tmpFile, srcFile)
				}
			},
		})
		testutil.FailErr(t, "New Core", err)
		defer func() { _ = core.ReleaseView() }()

		qp := QualifiedPath{RootID: "primary", Rel: "atomic.txt"}
		// Open for write without truncating so we can verify initial private bytes == captured base bytes!
		hid, _, err := core.Open(qp, OpenReadWrite, 0)
		testutil.FailErr(t, "Open atomic.txt with retry", err)

		privBuf := make([]byte, len(v2Bytes))
		n, err := core.ReadHandle(hid, privBuf, 0)
		testutil.FailErr(t, "ReadHandle initial private copy", err)
		testutil.FailErr(t, "CloseHandle", core.CloseHandle(hid))

		base, ok := core.CapturedBase(qp)
		if !ok {
			t.Fatal("expected captured base after successful retry")
		}
		v2Sum := sha256.Sum256(v2Bytes)
		if base.SHA256 != hex.EncodeToString(v2Sum[:]) || string(privBuf[:n]) != string(v2Bytes) {
			t.Fatalf("expected coherent v2 base and private copy; got base.SHA256=%s priv=%q", base.SHA256, string(privBuf[:n]))
		}
	})
}

// TestSymlinkRetargetingAndParentReplacementRejectsEscape proves that symlink escapes
// and parent directory replacements during resolution cannot escape the root boundary.
func TestSymlinkRetargetingAndParentReplacementRejectsEscape(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	outside := filepath.Join(tmp, "outside")
	backing := filepath.Join(tmp, "backing")
	for _, d := range []string{trunk, outside, backing} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(d, 0o755))
	}

	outsideSecret := filepath.Join(outside, "secret.txt")
	testutil.FailErr(t, "write outside secret", os.WriteFile(outsideSecret, []byte("do-not-touch"), 0o644))
	testutil.FailErr(t, "mkdir trunk/sub", os.MkdirAll(filepath.Join(trunk, "sub"), 0o755))
	testutil.FailErr(t, "symlink escape_rel", os.Symlink("../../outside/secret.txt", filepath.Join(trunk, "sub", "escape_rel")))
	testutil.FailErr(t, "symlink escape_abs", os.Symlink(outsideSecret, filepath.Join(trunk, "sub", "escape_abs")))

	// Hook that replaces a real directory `trunk/racedir` with a symlink to `outside` right before descriptor traversal opens it.
	testutil.FailErr(t, "mkdir trunk/racedir", os.MkdirAll(filepath.Join(trunk, "racedir"), 0o755))
	replaceRaceActive := false

	core, err := New(Config{
		ViewID:     "view-escape",
		BackingDir: backing,
		Roots: []RootSpec{
			{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true},
		},
		LookupRaceHook: func(_ QualifiedPath, component string) {
			if replaceRaceActive && component == "racedir" {
				_ = os.RemoveAll(filepath.Join(trunk, "racedir"))
				_ = os.Symlink(outside, filepath.Join(trunk, "racedir"))
			}
		},
	})
	testutil.FailErr(t, "New Core", err)
	defer func() { _ = core.ReleaseView() }()

	qp := func(rel string) QualifiedPath {
		return QualifiedPath{RootID: "primary", Rel: rel}
	}

	// 1. Relative symlink escaping root.
	if _, err := core.ReadFile(qp("sub/escape_rel")); !errors.Is(err, ErrSymlinkEscape) {
		t.Fatalf("expected ErrSymlinkEscape for relative escaping symlink, got %v", err)
	}
	if _, err := core.WriteFile(qp("sub/escape_rel"), []byte("pwn"), 0o644); !errors.Is(err, ErrSymlinkEscape) {
		t.Fatalf("expected ErrSymlinkEscape writing through relative escaping symlink, got %v", err)
	}

	// 2. Absolute symlink escaping root.
	if _, err := core.ReadFile(qp("sub/escape_abs")); !errors.Is(err, ErrSymlinkEscape) {
		t.Fatalf("expected ErrSymlinkEscape for absolute escaping symlink, got %v", err)
	}

	// 3. Parent directory replaced with escaping symlink mid-resolution.
	replaceRaceActive = true
	if _, err := core.WriteFile(qp("racedir/secret.txt"), []byte("pwn"), 0o644); !errors.Is(err, ErrSymlinkEscape) {
		t.Fatalf("expected ErrSymlinkEscape on parent directory replacement race, got %v", err)
	}

	// Verify outside secret is untouched.
	secretAfter, err := os.ReadFile(outsideSecret)
	testutil.FailErr(t, "read outside secret", err)
	if string(secretAfter) != "do-not-touch" {
		t.Fatalf("outside file was modified: %q", string(secretAfter))
	}
}

// virtual2MLowerProvider simulates a 2,000,000-entry lower root in O(1) memory
// to prove deterministic operation bounds without wall-clock sleeps.
type virtual2MLowerProvider struct {
	totalEntries int
}

func (v *virtual2MLowerProvider) Lstat(rel string) (LowerEntry, error) {
	rel = cleanRelPath(rel)
	if rel == "." || rel == "dir" {
		return LowerEntry{
			Kind:    EntryDirectory,
			Mode:    fs.ModeDir | 0o755,
			ModTime: time.Unix(1700000000, 0),
			Dev:     1,
			Ino:     1,
		}, nil
	}
	var idx int
	if _, err := fmt.Sscanf(rel, "dir/file_%07d.txt", &idx); err == nil && idx >= 0 && idx < v.totalEntries {
		content := fmt.Sprintf("content-%07d\n", idx)
		return LowerEntry{
			Kind:      EntryRegular,
			Mode:      0o644,
			Size:      int64(len(content)),
			ModTime:   time.Unix(1700000000, 0),
			Dev:       1,
			Ino:       uint64(100 + idx),
			CtimeSec:  1700000000,
			CtimeNsec: 0,
		}, nil
	}
	return LowerEntry{}, fs.ErrNotExist
}

func (v *virtual2MLowerProvider) ReadDir(rel string) ([]LowerDirEntry, error) {
	entries := make([]LowerDirEntry, v.totalEntries)
	for i := 0; i < v.totalEntries; i++ {
		entries[i] = LowerDirEntry{
			Name: fmt.Sprintf("file_%07d.txt", i),
			Kind: EntryRegular,
			Mode: 0o644,
			Ino:  uint64(100 + i),
		}
	}
	return entries, nil
}

func (v *virtual2MLowerProvider) OpenRead(rel string) (ReadSeekCloser, LowerEntry, error) {
	le, err := v.Lstat(rel)
	if err != nil {
		return nil, LowerEntry{}, err
	}
	var idx int
	_, _ = fmt.Sscanf(cleanRelPath(rel), "dir/file_%07d.txt", &idx)
	data := []byte(fmt.Sprintf("content-%07d\n", idx))
	return &bytesReadSeekCloser{Reader: bytes.NewReader(data)}, le, nil
}

func (v *virtual2MLowerProvider) Readlink(_ string) (string, error) {
	return "", fs.ErrInvalid
}

func (v *virtual2MLowerProvider) ListXattr(_ string) (map[string][]byte, error) {
	return nil, nil
}

type bytesReadSeekCloser struct {
	*bytes.Reader
}

func (b *bytesReadSeekCloser) Close() error { return nil }

var _ io.ReadCloser = (*bytesReadSeekCloser)(nil)

// TestScale2MLowerZeroDirReadsOnEnumeration proves that with a 2,000,000-entry lower
// and 100 private changes, neither applying the 100 changes nor enumerating/snapshotting
// the journal reads any lower directories (LowerDirReads == 0).
func TestScale2MLowerZeroDirReadsOnEnumeration(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	backing := filepath.Join(tmp, "backing")
	testutil.FailErr(t, "mkdir backing", os.MkdirAll(backing, 0o700))

	core, err := New(Config{
		ViewID:     "view-scale-2m",
		BackingDir: backing,
		Roots: []RootSpec{
			{ID: "primary", Label: "primary", IsPrimary: true},
		},
	})
	testutil.FailErr(t, "New Core", err)
	defer func() { _ = core.ReleaseView() }()

	vLower := &virtual2MLowerProvider{totalEntries: 2_000_000}
	testutil.FailErr(t, "RegisterLowerProvider", core.RegisterLowerProvider("primary", vLower))

	// Apply 100 private changes across the 2M-entry lower root.
	for i := 0; i < 100; i++ {
		target := QualifiedPath{
			RootID: "primary",
			Rel:    fmt.Sprintf("dir/file_%07d.txt", i*10_000),
		}
		_, err := core.WriteFile(target, []byte(fmt.Sprintf("modified-%d\n", i)), 0o644)
		testutil.FailErr(t, "WriteFile in 2M lower", err)
	}

	statsBeforeEnum := core.Stats()
	if statsBeforeEnum.LowerDirReads != 0 {
		t.Fatalf("expected 0 lower dir reads during 100 point mutations, got %d", statsBeforeEnum.LowerDirReads)
	}

	core.ResetStepStats()
	changes, err := core.EnumerateChanges(true)
	testutil.FailErr(t, "EnumerateChanges on 2M lower", err)
	if len(changes) != 100 {
		t.Fatalf("expected 100 logical changes, got %d", len(changes))
	}

	snap, err := core.SnapshotJournal()
	testutil.FailErr(t, "SnapshotJournal on 2M lower", err)
	if len(snap.Changes) != 100 {
		t.Fatalf("expected 100 snapshot changes, got %d", len(snap.Changes))
	}

	statsAfterEnum := core.Stats()
	if statsAfterEnum.LowerDirReads != 0 || statsAfterEnum.LowerLookups != 0 {
		t.Fatalf("journal enumeration must perform 0 lower directory reads and 0 lower lookups; got DirReads=%d Lookups=%d",
			statsAfterEnum.LowerDirReads, statsAfterEnum.LowerLookups)
	}
}

// TestConcurrentMutationsAndCheckpointBarrier exercises concurrent same-node and overlapping
// directory mutations under -race alongside BeginCheckpointBarrier / EndCheckpointBarrier.
func TestConcurrentMutationsAndCheckpointBarrier(t *testing.T) {
	t.Parallel()
	core, trunk, _ := newSingleRootTestCore(t)
	qp := func(rel string) QualifiedPath {
		return QualifiedPath{RootID: "primary", Rel: rel}
	}

	for i := 0; i < 16; i++ {
		name := fmt.Sprintf("shared_%02d.txt", i)
		testutil.FailErr(t, "seed trunk file", os.WriteFile(filepath.Join(trunk, name), []byte("initial-seed"), 0o644))
	}

	const workers = 8
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < 16; i++ {
				target := qp(fmt.Sprintf("shared_%02d.txt", i))
				_, err := core.WriteFile(target, []byte(fmt.Sprintf("worker-%d-item-%d", workerID, i)), 0o644)
				if err != nil && !errors.Is(err, ErrBarrierActive) {
					t.Errorf("unexpected WriteFile error: %v", err)
				}
				temp := qp(fmt.Sprintf("tmp_%d_%d.txt", workerID, i))
				if _, cErr := core.WriteFile(temp, []byte("renamed-payload"), 0o644); cErr == nil {
					_ = core.Rename(temp, target)
				}
			}
		}(w)
	}
	wg.Wait()

	// Acquire checkpoint barrier and prove new mutations are blocked with ErrBarrierActive while barrier holds.
	token, err := core.BeginCheckpointBarrier(context.Background())
	testutil.FailErr(t, "BeginCheckpointBarrier", err)

	if _, err := core.WriteFile(qp("blocked_during_barrier.txt"), []byte("no"), 0o644); !errors.Is(err, ErrBarrierActive) {
		t.Fatalf("expected ErrBarrierActive while checkpoint barrier is held, got %v", err)
	}

	changes, err := core.EnumerateChanges(true)
	testutil.FailErr(t, "EnumerateChanges under barrier", err)
	if len(changes) < 16 {
		t.Fatalf("expected at least 16 logical changes, got %d", len(changes))
	}

	testutil.FailErr(t, "EndCheckpointBarrier", core.EndCheckpointBarrier(token))

	// After barrier release, mutations succeed again.
	_, err = core.WriteFile(qp("after_barrier.txt"), []byte("ok"), 0o644)
	testutil.FailErr(t, "WriteFile after barrier release", err)
}
