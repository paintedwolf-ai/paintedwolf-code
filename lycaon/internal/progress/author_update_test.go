package progress

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPreparedAuthorUpdateHasNoEffectUntilApply(t *testing.T) {
	store := NewMemoryStore()
	content := "## Progress\n- [ ] Inspect source"
	prepared, err := PrepareAuthorUpdate(content)
	testutil.FailErr(t, "prepare progress", err)
	if store.Get(t.Context(), "session") != "" {
		t.Fatal("preparation committed progress")
	}
	result, err := prepared.Apply(t.Context(), store, "session", nil)
	testutil.FailErr(t, "apply prepared progress", err)
	if !result.Updated || store.Get(t.Context(), "session") != content {
		t.Fatalf("progress=%+v content=%q", result, store.Get(t.Context(), "session"))
	}
	result, err = prepared.ExpectRevision(result.Snapshot.Revision).ExpectItemIDs(result.Snapshot.ItemIDs...).Apply(t.Context(), store, "session", nil)
	testutil.FailErr(t, "reapply identical progress", err)
	if result.Updated {
		t.Fatal("identical progress created another update")
	}
}

func TestPreparedAuthorUpdateRejectsWithoutWrites(t *testing.T) {
	for _, content := range []string{"", "- [ ] outer\n  - [ ] nested", strings.Repeat("- [ ] step\n", MaxAuthorProgressLines+1)} {
		if _, err := PrepareAuthorUpdate(content); err == nil {
			t.Fatalf("invalid progress admitted %q", content)
		}
	}
	_, err := PrepareAuthorUpdate("- [ ] outer\n  - [ ] nested")
	var reject *AuthorUpdateRejection
	if !errors.As(err, &reject) || reject.Code != "PROGRESS_NESTED_LINE" {
		t.Fatalf("lost structure rejection: %v", err)
	}
	prepared, err := PrepareAuthorUpdate("- [ ] work")
	testutil.FailErr(t, "prepare valid progress", err)
	store := NewMemoryStore()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := prepared.Apply(ctx, store, "session", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled apply: %v", err)
	}
	if _, err := prepared.Apply(t.Context(), store, "", nil); err == nil {
		t.Fatal("missing session admitted")
	}
	if _, err := (AuthorUpdate{}).Apply(t.Context(), store, "session", nil); err == nil {
		t.Fatal("unprepared update admitted")
	}
	if store.Get(t.Context(), "session") != "" {
		t.Fatal("refused update committed")
	}
}

func TestPreparedAuthorUpdateUsesTheAppliedSessionHistory(t *testing.T) {
	prepared, err := PrepareAuthorUpdate("## Progress\n- [x] Inspect source")
	testutil.FailErr(t, "prepare completed checklist", err)
	store := NewMemoryStore()
	var rejected *AuthorUpdateRejection
	if _, err := prepared.Apply(t.Context(), store, "session", nil); !errors.As(err, &rejected) || rejected.Code != "PROGRESS_NO_OPEN_STEP" {
		t.Fatalf("initial completion rejection: %v", err)
	}
	if got := store.Get(t.Context(), "session"); got != "" {
		t.Fatalf("refused initial completion wrote %q", got)
	}
	testutil.FailErr(t, "install existing open checklist", store.Set("session", "## Progress\n- [ ] Inspect source"))
	observed, err := store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "observe existing checklist", err)
	result, err := prepared.ExpectRevision(observed.Revision).ExpectItemIDs(observed.ItemIDs...).Apply(t.Context(), store, "session", nil)
	testutil.FailErr(t, "complete the existing checklist", err)
	if !result.Updated || store.Get(t.Context(), "session") != "## Progress\n- [x] Inspect source" {
		t.Fatalf("completed checklist: %+v / %q", result, store.Get(t.Context(), "session"))
	}
}

func TestAuthorUpdateRequiresAndPreservesStableItemIDs(t *testing.T) {
	store := NewMemoryStore()
	first, err := PrepareAuthorUpdate("- [ ] First\n- [ ] Second")
	testutil.FailErr(t, "prepare initial checklist", err)
	created, err := first.Apply(t.Context(), store, "session", nil)
	testutil.FailErr(t, "create initial checklist", err)
	if len(created.Snapshot.ItemIDs) != 2 || created.Snapshot.ItemIDs[0] == "" || created.Snapshot.ItemIDs[1] == "" || created.Snapshot.ItemIDs[0] == created.Snapshot.ItemIDs[1] {
		t.Fatalf("initial item IDs = %v", created.Snapshot.ItemIDs)
	}
	id1, id2 := created.Snapshot.ItemIDs[0], created.Snapshot.ItemIDs[1]

	// Missing item_ids on an existing checklist must reject with PROGRESS_REVISION_REQUIRED.
	replace, err := PrepareAuthorUpdate("- [x] First\n- [ ] Second")
	testutil.FailErr(t, "prepare replacement", err)
	_, err = replace.ExpectRevision(created.Snapshot.Revision).Apply(t.Context(), store, "session", nil)
	var rejected *AuthorUpdateRejection
	if !errors.As(err, &rejected) || rejected.Code != "PROGRESS_REVISION_REQUIRED" {
		t.Fatalf("missing item_ids outcome = %v, want PROGRESS_REVISION_REQUIRED", err)
	}
	snap, ok := rejected.Data["snapshot"].(Snapshot)
	if !ok || !snap.Equal(created.Snapshot) {
		t.Fatalf("missing item_ids recovery snapshot = %+v", rejected.Data)
	}

	// Unknown, duplicate, or misaligned item_ids must reject with PROGRESS_REVISION_CHANGED.
	for _, badIDs := range [][]string{
		{"unknown-id", id2},
		{id1, id1},
		{id1},
		{id1, id2, ""},
	} {
		_, err = replace.ExpectRevision(created.Snapshot.Revision).ExpectItemIDs(badIDs...).Apply(t.Context(), store, "session", nil)
		if !errors.As(err, &rejected) || rejected.Code != "PROGRESS_REVISION_CHANGED" {
			t.Fatalf("bad item_ids %v outcome = %v, want PROGRESS_REVISION_CHANGED", badIDs, err)
		}
	}

	// Reorder + relabel + add new row (empty/null ID) preserves existing IDs and mints a fresh ID.
	reordered, err := PrepareAuthorUpdate("- [ ] Second relabeled\n- [ ] Added third\n- [x] First relabeled")
	testutil.FailErr(t, "prepare reordered replacement", err)
	updated, err := reordered.ExpectRevision(created.Snapshot.Revision).ExpectItemIDsFromArgs(map[string]any{
		"item_ids": []any{id2, nil, id1},
	}).Apply(t.Context(), store, "session", nil)
	testutil.FailErr(t, "apply reordered replacement", err)
	if !updated.Updated || !updated.StateChanged || !updated.DeliverableClosed {
		t.Fatalf("reordered replacement outcome = %+v", updated)
	}
	if len(updated.Snapshot.ItemIDs) != 3 || updated.Snapshot.ItemIDs[0] != id2 || updated.Snapshot.ItemIDs[2] != id1 {
		t.Fatalf("preserved item IDs = %v, want [%s, <new>, %s]", updated.Snapshot.ItemIDs, id2, id1)
	}
	id3 := updated.Snapshot.ItemIDs[1]
	if id3 == "" || id3 == id1 || id3 == id2 {
		t.Fatalf("minted item ID = %q", id3)
	}
}
