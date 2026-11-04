package layeredfs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type prefixFailReader struct {
	r         io.Reader
	limit     int
	readSoFar int
	err       error
}

func (p *prefixFailReader) Read(buf []byte) (int, error) {
	if p.readSoFar >= p.limit {
		return 0, p.err
	}
	rem := p.limit - p.readSoFar
	if len(buf) > rem {
		buf = buf[:rem]
	}
	n, err := p.r.Read(buf)
	p.readSoFar += n
	if p.readSoFar >= p.limit {
		return n, p.err
	}
	return n, err
}

// TestWorkingFileHashOpenAndPrefixReadFailurePropagates verifies Finding 5:
// working-file open failures and mid-read io.Copy failures during EnumerateChanges and SnapshotJournal
// are propagated rather than swallowed into empty or truncated digests.
func TestWorkingFileHashOpenAndPrefixReadFailurePropagates(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	backing := filepath.Join(tmp, "backing")
	testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
	testutil.FailErr(t, "mkdir backing", os.MkdirAll(backing, 0o700))

	mode := "none"
	core, err := New(Config{
		ViewID:     "view-hash-err",
		BackingDir: backing,
		Roots: []RootSpec{
			{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true},
		},
		FaultHooks: &FaultHooks{
			WorkingFileHashReader: func(qpath QualifiedPath, r io.Reader) (io.Reader, error) {
				if qpath.Rel == "file.txt" && mode == "prefix-err" {
					return &prefixFailReader{r: r, limit: 4, err: syscall.EIO}, nil
				}
				return r, nil
			},
		},
	})
	testutil.FailErr(t, "New Core", err)
	defer func() { _ = core.ReleaseView() }()

	qp := QualifiedPath{RootID: "primary", Rel: "file.txt"}
	_, err = core.WriteFile(qp, []byte("0123456789abcdef"), 0o644)
	testutil.FailErr(t, "WriteFile file.txt", err)

	mode = "prefix-err"
	if _, err := core.EnumerateChanges(true); !errors.Is(err, syscall.EIO) {
		t.Fatalf("expected syscall.EIO from EnumerateChanges on prefix read failure, got %v", err)
	}
	if _, err := core.SnapshotJournal(); !errors.Is(err, syscall.EIO) {
		t.Fatalf("expected syscall.EIO from SnapshotJournal on prefix read failure, got %v", err)
	}

	if st := core.Stats(); st.LogicalChanges != 1 {
		t.Fatalf("expected Stats().LogicalChanges == 1 without hashing working file, got %d", st.LogicalChanges)
	}

	mode = "none"
	entries, err := os.ReadDir(filepath.Join(backing, "working"))
	testutil.FailErr(t, "ReadDir working", err)
	for _, e := range entries {
		testutil.FailErr(t, "remove working blob", os.Remove(filepath.Join(backing, "working", e.Name())))
	}
	if _, err := core.EnumerateChanges(true); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected fs.ErrNotExist from EnumerateChanges when working file is missing, got %v", err)
	}
	if _, err := core.SnapshotJournal(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected fs.ErrNotExist from SnapshotJournal when working file is missing, got %v", err)
	}
}

// TestRenameHardLinkAliasOverSameNodeLeavesBothIntact verifies Finding 6:
// renaming one hard-link alias over another that points to the same private node is a no-op
// that preserves both names, Nlink == 2, and the change journal.
func TestRenameHardLinkAliasOverSameNodeLeavesBothIntact(t *testing.T) {
	t.Parallel()
	core, trunk, _ := newSingleRootTestCore(t)
	qp := func(rel string) QualifiedPath {
		return QualifiedPath{RootID: "primary", Rel: rel}
	}
	testutil.FailErr(t, "write trunk orig.txt", os.WriteFile(filepath.Join(trunk, "orig.txt"), []byte("hardlink-content"), 0o644))

	aliasA := qp("orig.txt")
	aliasB := qp("alias.txt")
	_, err := core.Link(aliasA, aliasB)
	testutil.FailErr(t, "Link orig.txt -> alias.txt", err)

	testutil.FailErr(t, "Rename aliasA over aliasB", core.Rename(aliasA, aliasB))

	attrA, err := core.Stat(aliasA)
	testutil.FailErr(t, "Stat aliasA after same-node rename", err)
	attrB, err := core.Stat(aliasB)
	testutil.FailErr(t, "Stat aliasB after same-node rename", err)
	if attrA.NodeID != attrB.NodeID || attrA.Nlink != 2 || attrB.Nlink != 2 {
		t.Fatalf("expected both aliases intact with shared NodeID and Nlink=2, got A=%+v B=%+v", attrA, attrB)
	}

	changes, err := core.EnumerateChanges(true)
	testutil.FailErr(t, "EnumerateChanges(true)", err)
	if len(changes) != 2 {
		t.Fatalf("expected 2 logical change entries (both hard-link aliases), got %+v", changes)
	}
	for _, ce := range changes {
		if ce.Deleted || len(ce.Final.HardLinkPaths) != 2 {
			t.Fatalf("expected non-deleted hard-link change entry with 2 paths, got %+v", ce)
		}
	}
}

func waitUntilAdmissionClosed(c *Core) {
	probe := QualifiedPath{RootID: "primary", Rel: "__admission_probe__"}
	for {
		_, err := c.Stat(probe)
		if errors.Is(err, ErrViewReleased) {
			return
		}
		runtime.Gosched()
	}
}

// TestReleaseDrainsInFlightWriteAccountingAndPreservesRecoveryState verifies Round 2 review Issue 1:
// ReleaseView separates admission closure from final release, drains already-admitted WriteHandle,
// WriteViaNodeHandle (including partial writes), and read/descriptor operations through their
// accounting/settlement before closing handles/descriptors, and preserves settled recovery state.
func TestReleaseDrainsInFlightWriteAccountingAndPreservesRecoveryState(t *testing.T) {
	t.Parallel()

	t.Run("write_handle_paused_after_bytes_written_commits_accounting_before_release_completes", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		trunk := filepath.Join(tmp, "trunk")
		backing := filepath.Join(tmp, "backing")
		testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
		testutil.FailErr(t, "mkdir backing", os.MkdirAll(backing, 0o700))

		bytesWrittenCh := make(chan struct{})
		allowCommitCh := make(chan struct{})
		var pauseOnce sync.Once

		core, err := New(Config{
			ViewID:     "view-release-writehandle",
			BackingDir: backing,
			Roots: []RootSpec{
				{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true},
			},
			FaultHooks: &FaultHooks{
				WriteAtFunc: func(qpath QualifiedPath, file *os.File, data []byte, offset int64) (int, error) {
					n, wErr := file.WriteAt(data, offset)
					if qpath.Rel == "inflight.txt" {
						pauseOnce.Do(func() {
							close(bytesWrittenCh)
							<-allowCommitCh
						})
					}
					return n, wErr
				},
			},
		})
		testutil.FailErr(t, "New Core", err)

		qp := QualifiedPath{RootID: "primary", Rel: "inflight.txt"}
		hid, _, err := core.Open(qp, OpenReadWrite|OpenCreate, 0o644)
		testutil.FailErr(t, "Open inflight.txt", err)

		payload := []byte("committed-across-release-drain")
		var writeN int
		var writeErr error
		writeDone := make(chan struct{})
		go func() {
			defer close(writeDone)
			writeN, writeErr = core.WriteHandle(hid, payload, 0)
		}()

		<-bytesWrittenCh

		releaseDone := make(chan error, 1)
		go func() {
			releaseDone <- core.ReleaseView()
		}()

		waitUntilAdmissionClosed(core)

		// New mutations while release is draining must be rejected with ErrViewReleased.
		if _, err := core.WriteFile(QualifiedPath{RootID: "primary", Rel: "late.txt"}, []byte("no"), 0o644); !errors.Is(err, ErrViewReleased) {
			t.Fatalf("expected ErrViewReleased for new WriteFile during release drain, got %v", err)
		}

		close(allowCommitCh)
		<-writeDone
		testutil.FailErr(t, "in-flight WriteHandle", writeErr)
		if writeN != len(payload) {
			t.Fatalf("expected writeN=%d, got %d", len(payload), writeN)
		}
		testutil.FailErr(t, "ReleaseView", <-releaseDone)

		// Held handle is now invalidated after release completes.
		if _, err := core.ReadHandle(hid, make([]byte, 4), 0); !errors.Is(err, ErrViewReleased) {
			t.Fatalf("expected ErrViewReleased on ReadHandle after release, got %v", err)
		}

		// Recovery state must preserve the committed write accounting and digest.
		wantSum := sha256.Sum256(payload)
		wantHex := hex.EncodeToString(wantSum[:])

		changes, err := core.EnumerateChanges(true)
		testutil.FailErr(t, "EnumerateChanges after ReleaseView", err)
		if len(changes) != 1 || changes[0].Final.Size != int64(len(payload)) || changes[0].Final.SHA256 != wantHex {
			t.Fatalf("expected settled ChangeEntry with size=%d sha=%s, got %+v", len(payload), wantHex, changes)
		}
		persisted, err := core.LoadPersistedJournal()
		testutil.FailErr(t, "LoadPersistedJournal after ReleaseView", err)
		if len(persisted.Changes) != 1 || persisted.Changes[0].Final.Size != int64(len(payload)) {
			t.Fatalf("expected persisted journal change size=%d, got %+v", len(payload), persisted.Changes)
		}
		ops := core.JournalOperations()
		hasWriteOp := false
		for _, op := range ops {
			if op.Kind == JournalOpWrite && op.Path == qp && op.Size == int64(len(payload)) {
				hasWriteOp = true
			}
		}
		if !hasWriteOp {
			t.Fatalf("expected JournalOpWrite with size=%d in journal operations, got %+v", len(payload), ops)
		}
	})

	t.Run("write_via_node_handle_partial_write_paused_during_release_preserves_partial_accounting", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		trunk := filepath.Join(tmp, "trunk")
		backing := filepath.Join(tmp, "backing")
		testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
		testutil.FailErr(t, "mkdir backing", os.MkdirAll(backing, 0o700))

		bytesWrittenCh := make(chan struct{})
		allowCommitCh := make(chan struct{})
		var pauseOnce sync.Once

		core, err := New(Config{
			ViewID:     "view-release-nodehandle",
			BackingDir: backing,
			Roots: []RootSpec{
				{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true},
			},
			FaultHooks: &FaultHooks{
				WriteAtFunc: func(qpath QualifiedPath, file *os.File, data []byte, offset int64) (int, error) {
					if qpath.Rel == "ro_node.txt" {
						half := len(data) / 2
						n, _ := file.WriteAt(data[:half], offset)
						pauseOnce.Do(func() {
							close(bytesWrittenCh)
							<-allowCommitCh
						})
						return n, syscall.ENOSPC
					}
					return file.WriteAt(data, offset)
				},
			},
		})
		testutil.FailErr(t, "New Core", err)

		qp := QualifiedPath{RootID: "primary", Rel: "ro_node.txt"}
		hid, _, err := core.CreateFile(qp, 0o444, true)
		testutil.FailErr(t, "CreateFile ro_node.txt", err)

		payload := []byte("0123456789ABCDEF")
		var n int
		var wErr error
		writeDone := make(chan struct{})
		go func() {
			defer close(writeDone)
			n, wErr = core.WriteHandle(hid, payload, 0)
		}()

		<-bytesWrittenCh

		releaseDone := make(chan error, 1)
		go func() {
			releaseDone <- core.ReleaseView()
		}()

		waitUntilAdmissionClosed(core)
		close(allowCommitCh)
		<-writeDone

		if !errors.Is(wErr, syscall.ENOSPC) || n != 8 {
			t.Fatalf("expected (8, ENOSPC) from in-flight WriteHandle, got (n=%d, err=%v)", n, wErr)
		}
		testutil.FailErr(t, "ReleaseView", <-releaseDone)

		wantSum := sha256.Sum256(payload[:8])
		changes, err := core.EnumerateChanges(true)
		testutil.FailErr(t, "EnumerateChanges after ReleaseView", err)
		if len(changes) != 1 || changes[0].Final.Size != 8 || changes[0].Final.SHA256 != hex.EncodeToString(wantSum[:]) {
			t.Fatalf("expected partial write accounting preserved after ReleaseView, got %+v", changes)
		}
	})

	t.Run("in_flight_lower_read_drains_before_release_closes_descriptors", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		trunk := filepath.Join(tmp, "trunk")
		backing := filepath.Join(tmp, "backing")
		testutil.FailErr(t, "mkdir trunk", os.MkdirAll(trunk, 0o755))
		testutil.FailErr(t, "mkdir backing", os.MkdirAll(backing, 0o700))
		testutil.FailErr(t, "write trunk doc.txt", os.WriteFile(filepath.Join(trunk, "doc.txt"), []byte("hello-lower"), 0o644))

		inStatCh := make(chan struct{})
		allowStatFinishCh := make(chan struct{})
		var hookOnce sync.Once

		core, err := New(Config{
			ViewID:     "view-release-read-drain",
			BackingDir: backing,
			Roots: []RootSpec{
				{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true},
			},
			FaultHooks: &FaultHooks{
				LowerXattrHook: func(qpath QualifiedPath) error {
					if qpath.Rel == "doc.txt" {
						hookOnce.Do(func() {
							close(inStatCh)
							<-allowStatFinishCh
						})
					}
					return nil
				},
			},
		})
		testutil.FailErr(t, "New Core", err)

		statDone := make(chan error, 1)
		go func() {
			_, sErr := core.Stat(QualifiedPath{RootID: "primary", Rel: "doc.txt"})
			statDone <- sErr
		}()

		<-inStatCh
		releaseDone := make(chan error, 1)
		go func() {
			releaseDone <- core.ReleaseView()
		}()

		waitUntilAdmissionClosed(core)
		close(allowStatFinishCh)
		testutil.FailErr(t, "in-flight Stat", <-statDone)
		testutil.FailErr(t, "ReleaseView", <-releaseDone)
	})
}
