package privategit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

// Existence and inventory counts cannot prove that a retained commit's trees and blobs survived.
func TestInterruptedImportRollbackPreservesReadableLeasedHistory(t *testing.T) {
	source := t.TempDir()
	gittest.Init(t, source)
	file := filepath.Join(source, "payload.txt")
	testutil.FailErr(t, "first content", os.WriteFile(file, []byte("first version\n"), 0o644))
	gittest.CommitAll(t, source, "first")
	first := strings.TrimSpace(gittest.Run(t, source, "rev-parse", "HEAD"))
	testutil.FailErr(t, "second content", os.WriteFile(file, []byte("second version\n"), 0o644))
	gittest.CommitAll(t, source, "second")
	second := strings.TrimSpace(gittest.Run(t, source, "rev-parse", "HEAD"))
	gittest.Run(t, source, "repack", "-a", "-d")
	root := t.TempDir()
	store, err := NewRetainedObjectStoreContext(t.Context(), root)
	testutil.FailErr(t, "open retained store", err)
	t.Cleanup(func() {
		if store != nil {
			_ = store.Close()
		}
	})
	_, reachable, err := store.ImportRepositoryObjects(t.Context(), []string{filepath.Join(source, ".git", "objects")}, []string{second})
	testutil.FailErr(t, "retain history", err)
	_, err = store.RetainForCheckpoint("leased-history", reachable)
	testutil.FailErr(t, "lease history", err)
	// The oracle has only the retained store as an object source, never the original repository.
	testutil.FailErr(t, "remove original fixture", os.RemoveAll(source))
	reader := t.TempDir()
	gittest.Init(t, reader)
	testutil.FailErr(t, "reader alternates", os.WriteFile(filepath.Join(reader, ".git", "objects", "info", "alternates"), []byte(store.ObjectsDir()+"\n"), 0o600))
	assertReadable := func() {
		t.Helper()
		for oid, want := range map[string]string{first: "first version", second: "second version"} {
			if got := strings.TrimSpace(gittest.Run(t, reader, "show", oid+":payload.txt")); got != want {
				t.Fatalf("retained commit %s lost its payload: %q", oid, got)
			}
		}
		if got := strings.TrimSpace(gittest.Run(t, reader, "rev-parse", second+"^")); got != first {
			t.Fatalf("retained parent edge changed: %q", got)
		}
	}
	candidate := t.TempDir()
	gittest.Init(t, candidate)
	testutil.FailErr(t, "candidate content", os.WriteFile(filepath.Join(candidate, "other.txt"), []byte("unrelated\n"), 0o644))
	gittest.CommitAll(t, candidate, "candidate")
	gittest.Run(t, candidate, "repack", "-a", "-d")
	interrupted := errors.New("rollback intent unavailable")
	fault := withGCTestHooks(t.Context(), gcTestHooks{
		beforeRollbackMarkerWrite: func(string, string) error { return interrupted },
	})
	_, _, err = store.ImportRepositoryObjects(fault, []string{filepath.Join(candidate, ".git", "objects")}, []string{strings.Repeat("f", 40)})
	if !errors.Is(err, interrupted) {
		t.Fatalf("rollback interruption not exercised: %v", err)
	}
	assertReadable()
	_, err = store.CollectGarbage(t.Context())
	testutil.FailErr(t, "continued-use GC", err)
	assertReadable()
	testutil.FailErr(t, "close before recovery", store.Close())
	store, err = NewRetainedObjectStoreContext(t.Context(), root)
	testutil.FailErr(t, "reopen store", err)
	assertReadable()
}
