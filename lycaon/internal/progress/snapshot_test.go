package progress

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAuthorUpdateRevisionAdmitsOneConcurrentReplacement(t *testing.T) {
	store := NewMemoryStore()
	store.EnsureRun("session", "run", "Deliver change")
	initial, err := store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "read initial progress", err)
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, content := range []string{"- [ ] First", "- [ ] Second"} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			update, prepareErr := PrepareAuthorUpdate(content)
			if prepareErr != nil {
				results <- prepareErr
				return
			}
			_, applyErr := update.ExpectRevision(initial.Revision).Apply(t.Context(), store, "session", nil)
			results <- applyErr
		}()
	}
	wait.Wait()
	close(results)
	accepted, conflicted := 0, 0
	for err := range results {
		if err == nil {
			accepted++
			continue
		}
		var rejection *AuthorUpdateRejection
		if !errors.As(err, &rejection) || rejection.Code != "PROGRESS_REVISION_CHANGED" {
			t.Fatalf("unexpected update result: %v", err)
		}
		conflicted++
	}
	if accepted != 1 || conflicted != 1 {
		t.Fatalf("accepted=%d conflicts=%d", accepted, conflicted)
	}
}

func TestAuthorUpdateSeparatesDocumentAndDeliverableChanges(t *testing.T) {
	store := NewMemoryStore()
	apply := func(content string) AuthorUpdateResult {
		t.Helper()
		update, err := PrepareAuthorUpdate(content)
		testutil.FailErr(t, "prepare checklist", err)
		result, err := update.Apply(t.Context(), store, "session", nil)
		testutil.FailErr(t, "apply checklist", err)
		return result
	}
	first := apply("- [ ] Write code\n- [ ] Verify code")
	if !first.Updated || first.StateChanged || first.DeliverableClosed {
		t.Fatalf("establishment outcome: %+v", first)
	}
	noop := apply(first.Snapshot.Content)
	if noop.Updated || noop.StateChanged || noop.DeliverableClosed || noop.Snapshot.Revision != first.Snapshot.Revision {
		t.Fatalf("no-op outcome: %+v", noop)
	}
	label := apply("- [ ] Implement code\n- [ ] Verify code")
	if !label.Updated || label.StateChanged || label.DeliverableClosed {
		t.Fatalf("label change outcome: %+v", label)
	}
	closed := apply("- [x] Implement code\n- [ ] Verify code")
	if !closed.Updated || !closed.StateChanged || !closed.DeliverableClosed {
		t.Fatalf("terminal transition outcome: %+v", closed)
	}
}

func TestSnapshotRevisionIncludesBoundRun(t *testing.T) {
	first := NewSnapshot("first", "- [ ] Deliver")
	second := NewSnapshot("second", first.Content)
	if first.Revision == second.Revision {
		t.Fatal("different run inherited revision")
	}
	if NewSnapshot(first.RunID, first.Content).Revision != first.Revision {
		t.Fatal("revision depends on process-local state")
	}
}

func TestAuthorUpdateRejectsOversizeWithoutTruncatingState(t *testing.T) {
	store := NewMemoryStore()
	testutil.FailErr(t, "seed checklist", store.Set("session", "- [ ] Keep complete"))
	_, err := PrepareAuthorUpdate("- [ ] " + strings.Repeat("x", ContentMaxChars))
	var rejected *AuthorUpdateRejection
	if !errors.As(err, &rejected) || rejected.Code != "PROGRESS_DOCUMENT_TOO_LARGE" {
		t.Fatalf("oversize outcome: %v", err)
	}
	if store.Get(t.Context(), "session") != "- [ ] Keep complete" {
		t.Fatal("oversized proposal changed stored state")
	}
}

func TestDeliverableTransitionsAreInvariantUnderRowPermutation(t *testing.T) {
	initial := "- [ ] Same label\n- [x] Same label\n- [ ] Other"
	permuted := "- [ ] Other\n- [x] Same label\n- [ ] Same label"
	if result := ClassifyTransitions(initial, permuted); result.Changed || result.Closed {
		t.Fatalf("permutation invented a transition: %+v", result)
	}
	for _, extra := range []string{"- [ ] Same label", "- [x] Same label", "- [ ] New scope"} {
		if result := ClassifyTransitions(initial, permuted+"\n"+extra); result.Changed || result.Closed {
			t.Fatalf("new row invented a transition: %+v", result)
		}
	}
	completed := "- [x] Same label\n- [ ] Other\n- [x] Same label"
	if result := ClassifyTransitions(initial, completed); !result.Changed || !result.Closed {
		t.Fatalf("terminal transition lost: %+v", result)
	}
}
