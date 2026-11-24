package progress

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMixedAddRemoveAnchorAndMultiAddOrder(t *testing.T) {
	store := newProgressFixture(t, "session")
	seeded, err := store.Apply(t.Context(), "session", Mutation{
		Replace: []RowSpec{
			{Label: "First", State: ProgressStatePending},
			{Label: "Second", State: ProgressStatePending},
			{Label: "Note", State: ProgressStateOptional},
		},
	}, ApplyOptions{})
	testutil.FailErr(t, "seed plan", err)
	if !slices.Equal(seeded.AddedItemIDs, []string{"p1", "p2", "p3"}) {
		t.Fatalf("added IDs = %v", seeded.AddedItemIDs)
	}

	// Anchoring an addition onto a row removed in the same call must reject.
	_, err = PrepareMutation(map[string]any{
		"remove": []any{"p2"},
		"add":    []any{map[string]any{"label": "After removed", "after_id": "p2"}},
	})
	var conflict *MutationRejection
	if !errors.As(err, &conflict) || conflict.Code != "PROGRESS_CONFLICTING_OPERATIONS" {
		t.Fatalf("removed anchor outcome = %v, want PROGRESS_CONFLICTING_OPERATIONS", err)
	}

	// Multiple additions sharing an anchor (start, surviving p1, and default tail) preserve input order.
	mixed, err := PrepareMutation(map[string]any{
		"remove": []any{"p2"},
		"add": []any{
			map[string]any{"label": "Start A", "after_id": "start"},
			map[string]any{"label": "Start B", "after_id": "start"},
			map[string]any{"label": "After p1 A", "after_id": "p1"},
			map[string]any{"label": "After p1 B", "after_id": "p1"},
			map[string]any{"label": "Tail A"},
			map[string]any{"label": "Tail B"},
		},
	})
	testutil.FailErr(t, "prepare mixed edit", err)
	out, err := store.Apply(t.Context(), "session", mixed, ApplyOptions{})
	testutil.FailErr(t, "apply mixed edit", err)

	var labels []string
	var ids []string
	for _, r := range out.Snapshot.Rows {
		labels = append(labels, r.Label)
		ids = append(ids, r.ID)
	}
	wantLabels := []string{"Start A", "Start B", "First", "After p1 A", "After p1 B", "Tail A", "Tail B", "Note"}
	wantIDs := []string{"p4", "p5", "p1", "p6", "p7", "p8", "p9", "p3"}
	if !slices.Equal(labels, wantLabels) || !slices.Equal(ids, wantIDs) {
		t.Fatalf("rows = %v (%v), want %v (%v)", labels, ids, wantLabels, wantIDs)
	}
}

func TestRequestSnapshotContainsNoRowUUIDsOrRevisionHashes(t *testing.T) {
	var rows []Row
	label := strings.Repeat("x", MaxLabelRunes)
	for i := 0; i < MaxAuthorProgressRows; i++ {
		rows = append(rows, Row{ID: "p1", State: ProgressStatePending, Label: label})
	}
	snap := NewSnapshot(uuid.NewString(), rows...)
	snap.Reconciliation = &Reconciliation{
		Jobs:        []string{"job-secret-1", "job-secret-2"},
		Settled:     []string{"`scout` (repo-researcher): survey"},
		Unaccounted: []UnaccountedResult{{Handle: "scout#1", Worker: "scout", Summary: "survey"}},
	}
	view := ForRequest("session-scope", snap)
	raw, err := json.Marshal(view)
	testutil.FailErr(t, "marshal request snapshot", err)

	hex64 := regexp.MustCompile(`[0-9a-f]{64}`)
	uuidPattern := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	if hex64.Match(raw) || uuidPattern.Match(raw) || bytes.Contains(raw, []byte("job-secret")) {
		t.Fatalf("request snapshot leaked internal hash/UUID/job: %s", raw)
	}
	const maxSnapshotPromptBytes = 8192
	if len(raw) > maxSnapshotPromptBytes {
		t.Fatalf("serialized request snapshot size = %d, exceeds %d", len(raw), maxSnapshotPromptBytes)
	}
}

func TestApplySeparatesDocumentAndDeliverableChanges(t *testing.T) {
	store := newProgressFixture(t, "session")
	first, err := store.Apply(t.Context(), "session", Mutation{
		Replace: []RowSpec{
			{Label: "Write code", State: ProgressStatePending},
			{Label: "Verify code", State: ProgressStatePending},
		},
	}, ApplyOptions{})
	testutil.FailErr(t, "seed rows", err)
	if !first.Updated || first.StateChanged || len(first.ClosedItemIDs) != 0 {
		t.Fatalf("establishment outcome: %+v", first)
	}

	pending := ProgressStatePending
	noop, err := store.Apply(t.Context(), "session", Mutation{
		Update: []UpdateRowSpec{{ID: "p1", State: &pending}},
	}, ApplyOptions{})
	testutil.FailErr(t, "noop update", err)
	if noop.Updated || noop.StateChanged || len(noop.ClosedItemIDs) != 0 || noop.Snapshot.DocumentVersion() != first.Snapshot.DocumentVersion() {
		t.Fatalf("no-op outcome: %+v", noop)
	}

	relabeled := "Implement code"
	labelOut, err := store.Apply(t.Context(), "session", Mutation{
		Update: []UpdateRowSpec{{ID: "p1", Label: &relabeled}},
	}, ApplyOptions{})
	testutil.FailErr(t, "relabel update", err)
	if !labelOut.Updated || labelOut.StateChanged || len(labelOut.ClosedItemIDs) != 0 {
		t.Fatalf("label change outcome: %+v", labelOut)
	}

	done := ProgressStateDone
	closed, err := store.Apply(t.Context(), "session", Mutation{
		Update: []UpdateRowSpec{{ID: "p1", State: &done}},
	}, ApplyOptions{})
	testutil.FailErr(t, "close update", err)
	if !closed.Updated || !closed.StateChanged || !slices.Equal(closed.ClosedItemIDs, []string{"p1"}) {
		t.Fatalf("terminal transition outcome: %+v", closed)
	}
}

func TestDeliverableTransitionsAreInvariantUnderRowPermutation(t *testing.T) {
	initial := NewSnapshot("run",
		Row{ID: "p1", State: ProgressStatePending, Label: "Same label"},
		Row{ID: "p2", State: ProgressStateDone, Label: "Same label"},
		Row{ID: "p3", State: ProgressStatePending, Label: "Other"},
	)
	permuted := NewSnapshot("run",
		Row{ID: "p3", State: ProgressStatePending, Label: "Other"},
		Row{ID: "p2", State: ProgressStateDone, Label: "Same label"},
		Row{ID: "p1", State: ProgressStatePending, Label: "Same label"},
	)
	if result := ClassifyTransitions(initial, permuted); result.Changed || len(result.ClosedItemIDs) != 0 {
		t.Fatalf("permutation invented a transition: %+v", result)
	}
	withExtra := NewSnapshot("run", append(slices.Clone(permuted.Rows), Row{ID: "p4", State: ProgressStateDone, Label: "New"})...)
	if result := ClassifyTransitions(initial, withExtra); result.Changed || len(result.ClosedItemIDs) != 0 {
		t.Fatalf("new row invented a transition: %+v", result)
	}
	completed := NewSnapshot("run",
		Row{ID: "p1", State: ProgressStateDone, Label: "Same label"},
		Row{ID: "p3", State: ProgressStatePending, Label: "Other"},
		Row{ID: "p2", State: ProgressStateDone, Label: "Same label"},
	)
	if result := ClassifyTransitions(initial, completed); !result.Changed || !slices.Equal(result.ClosedItemIDs, []string{"p1"}) {
		t.Fatalf("terminal transition lost: %+v", result)
	}
}

func TestProgressSummarySeparatesTerminalAndCompleted(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows []Row
		want Summary
	}{
		{"empty", nil, Summary{}},
		{"partial", []Row{{ID: "p1", State: "done", Label: "Build"}, {ID: "p2", State: "pending", Label: "Verify"}}, Summary{Pending: 1, Completed: 1}},
		{"complete", []Row{{ID: "p1", State: "done", Label: "Build"}, {ID: "p2", State: "done", Label: "Verify"}}, Summary{Completed: 2, AllRequiredItemsTerminal: true, AllRequiredItemsCompleted: true}},
		{"not pursued", []Row{{ID: "p1", State: "done", Label: "Build"}, {ID: "p2", State: "na", Label: "Verify"}}, Summary{Completed: 1, NotPursued: 1, AllRequiredItemsTerminal: true}},
		{"optional only", []Row{{ID: "p1", State: "optional", Label: "Later"}}, Summary{Optional: 1, AllRequiredItemsTerminal: true}},
		{"optional remaining", []Row{{ID: "p1", State: "done", Label: "Build"}, {ID: "p2", State: "optional", Label: "Later"}}, Summary{Completed: 1, Optional: 1, AllRequiredItemsTerminal: true, AllRequiredItemsCompleted: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := ForRequest("session", NewSnapshot("run", tc.rows...))
			if view.Summary != tc.want || AllTerminal(tc.rows) != tc.want.AllRequiredItemsTerminal {
				t.Fatalf("summary = %+v, want %+v", view.Summary, tc.want)
			}
			done, pending, na := CloseCounts(tc.rows)
			if done != tc.want.Completed || pending != tc.want.Pending || na != tc.want.NotPursued {
				t.Fatalf("closure counts = %d/%d/%d", done, pending, na)
			}
		})
	}
}
