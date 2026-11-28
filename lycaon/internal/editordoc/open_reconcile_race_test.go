package editordoc

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
)

func waitForDocumentLockWaiter(t *testing.T, locks *keyedLocks, key string, wantRefs int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		locks.mu.Lock()
		l := locks.locks[key]
		refs := 0
		if l != nil {
			refs = l.refs
		}
		locks.mu.Unlock()
		if refs >= wantRefs {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("timed out waiting for %d refs on lock key %q", wantRefs, key)
}

// TestOpenReloadsDocumentUnderLockAcrossConcurrentReconcile proves that Open
// never reconciles or projects a stale Document snapshot read before acquiring
// documentLockKey(d.ID) — which previously failed with
// "accepted document text does not match its CRDT journal".
func TestOpenReloadsDocumentUnderLockAcrossConcurrentReconcile(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "top\nmiddle alpha\nbottom\n"})
	opened := f.open(t, "a.txt")

	drafted, err := f.service.ReplaceSnapshot(t.Context(), opened.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{
			ClientID:         "cfs-driver",
			OperationID:      uuid.NewString(),
			ExpectedRevision: opened.Revision,
		},
		Content:  "top [draft]\nmiddle alpha\nbottom\n",
		EOL:      "lf",
		MixedEOL: false,
	})
	testutil.FailErr(t, "inject dirty draft", err)
	if !drafted.Dirty || drafted.Revision != 2 {
		t.Fatalf("drafted = %+v", drafted)
	}

	afterBytes := "top\nmiddle beta\nbottom\n"
	f.write(t, "a.txt", afterBytes)

	// Hold the document lock so concurrent Open reads revision 2 via GetByIdentity
	// and then blocks waiting on documentLockKey(drafted.ID).
	lockKey := documentLockKey(drafted.ID)
	unlockDoc := f.service.docLocks.lock(lockKey)

	type openResult struct {
		doc *Document
		err error
	}
	done := make(chan openResult, 1)
	go func() {
		d, openErr := f.service.Open(t.Context(), f.project, "a.txt", f.rootID, "", "cfs-driver", nil)
		done <- openResult{doc: d, err: openErr}
	}()

	waitForDocumentLockWaiter(t, &f.service.docLocks, lockKey, 2)

	// Advance the document + CRDT journal to revision 3 while Open is waiting on the doc lock.
	current, err := f.store.Get(t.Context(), drafted.ID)
	testutil.FailErr(t, "load current doc", err)
	testutil.FailErr(t, "reconcile disk while open waits", f.service.reconcileDocumentDisk(t.Context(), f.project, current, ""))
	unlockDoc()

	res := <-done
	testutil.FailErr(t, "concurrent reopen must not fail with CRDT journal mismatch", res.err)

	wantHashBytes := sha256.Sum256([]byte(afterBytes))
	wantHash := hex.EncodeToString(wantHashBytes[:])
	if res.doc.ID != drafted.ID || res.doc.FileID != drafted.FileID {
		t.Fatalf("reopened identity changed: got id=%s file_id=%s", res.doc.ID, res.doc.FileID)
	}
	if res.doc.Revision != 3 || !res.doc.Dirty {
		t.Fatalf("reopened state = rev=%d dirty=%v, want rev=3 dirty=true", res.doc.Revision, res.doc.Dirty)
	}
	if res.doc.Draft != "top [draft]\nmiddle beta\nbottom\n" {
		t.Fatalf("reopened draft = %q, want %q", res.doc.Draft, "top [draft]\nmiddle beta\nbottom\n")
	}
	if res.doc.BaseContent != afterBytes || res.doc.BaseSHA256 != wantHash {
		t.Fatalf("reopened base = %q (%s), want %q (%s)", res.doc.BaseContent, res.doc.BaseSHA256, afterBytes, wantHash)
	}
}

// TestOpenAbsentReloadsDocumentUnderLockAcrossConcurrentReconcile proves the
// same reload-under-lock invariant for openAbsentDocument.
func TestOpenAbsentReloadsDocumentUnderLockAcrossConcurrentReconcile(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "top\nmiddle alpha\nbottom\n"})
	opened := f.open(t, "a.txt")

	drafted, err := f.service.ReplaceSnapshot(t.Context(), opened.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{
			ClientID:         "cfs-driver",
			OperationID:      uuid.NewString(),
			ExpectedRevision: opened.Revision,
		},
		Content:  "top [draft]\nmiddle alpha\nbottom\n",
		EOL:      "lf",
		MixedEOL: false,
	})
	testutil.FailErr(t, "inject dirty draft", err)

	// Delete the file so Open takes the openAbsentDocument path.
	testutil.FailErr(t, "remove a.txt", os.Remove(filepath.Join(f.root, "a.txt")))

	lockKey := documentLockKey(drafted.ID)
	unlockDoc := f.service.docLocks.lock(lockKey)

	type openResult struct {
		doc *Document
		err error
	}
	done := make(chan openResult, 1)
	go func() {
		d, openErr := f.service.Open(t.Context(), f.project, "a.txt", f.rootID, "", "cfs-driver", nil)
		done <- openResult{doc: d, err: openErr}
	}()

	waitForDocumentLockWaiter(t, &f.service.docLocks, lockKey, 2)

	current, err := f.store.Get(t.Context(), drafted.ID)
	testutil.FailErr(t, "load current doc", err)
	testutil.FailErr(t, "mark absent while open waits", f.service.markAbsent(t.Context(), current))
	unlockDoc()

	res := <-done
	testutil.FailErr(t, "concurrent absent reopen must not fail", res.err)
	if !res.doc.Absent || !res.doc.Dirty || res.doc.Draft != "top [draft]\nmiddle alpha\nbottom\n" {
		t.Fatalf("unexpected absent doc = %+v", res.doc)
	}
}
