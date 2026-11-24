package loopguard

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/progress/progresstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTerminalProgressRewordingStillRepeats(t *testing.T) {
	g := NewMemoryDoomLoopGuard()
	for i := range DoomLoopMaxAttempts {
		args := map[string]any{
			"update": []any{map[string]any{"id": "p1", "state": "done", "label": fmt.Sprintf("Finished %d", i)}},
		}
		testutil.FailErr(t, "record completed checklist", g.RecordAttempt(t.Context(), "session", uuid.NewString(), "update_progress", args, nil, false))
	}
	args := map[string]any{
		"update": []any{map[string]any{"id": "p1", "state": "done", "label": "Reworded completion"}},
	}
	allowed, count, _, err := g.Check(t.Context(), "session", uuid.NewString(), "update_progress", args, false, nil)
	testutil.FailErr(t, "check repeated completion", err)
	if allowed || count != DoomLoopMaxAttempts {
		t.Fatalf("allowed=%v count=%d", allowed, count)
	}
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"read", map[string]any{"path": "a.go"}},
		{"update_progress", map[string]any{"add": []any{map[string]any{"label": "Unrelated scope"}}}},
	} {
		testutil.FailErr(t, "record inspection or bookkeeping", g.RecordAttempt(t.Context(), "session", uuid.NewString(), tc.tool, tc.args, nil, false))
		allowed, _, _, err = g.Check(t.Context(), "session", uuid.NewString(), "update_progress", args, false, nil)
		testutil.FailErr(t, "check completion after nonadvancement", err)
		if allowed {
			t.Fatal("inspection or bookkeeping erased completed-checklist repetition")
		}
	}
	testutil.FailErr(t, "record real work", g.RecordAttempt(t.Context(), "session", uuid.NewString(), "write", map[string]any{"path": "main.go"}, nil, true))
	allowed, _, _, err = g.Check(t.Context(), "session", uuid.NewString(), "update_progress", args, false, nil)
	testutil.FailErr(t, "check completion after work", err)
	if !allowed {
		t.Fatal("real intervening work must permit another completed checklist")
	}
}

func TestProgressFingerprintKeepsPendingOps(t *testing.T) {
	pendingArgs := map[string]any{"replace": []any{map[string]any{"label": "Pending"}}}
	got := doomLoopUpdateProgressArgs(pendingArgs)
	if got["replace"] == nil {
		t.Fatalf("pending replace args lost: %+v", got)
	}
	if got := doomLoopUpdateProgressArgs(nil); len(got) != 0 {
		t.Fatalf("nil args = %+v", got)
	}
}

func TestTerminalProgressKeepsDistinctFinishAcknowledgements(t *testing.T) {
	guard := NewMemoryDoomLoopGuard()
	for n := range DoomLoopMaxAttempts {
		args := map[string]any{
			"update":              []any{map[string]any{"id": "p1", "state": "done", "label": fmt.Sprintf("Label %d", n)}},
			"acknowledge_results": []any{"fixer#1"},
		}
		testutil.FailErr(t, "record repeated acknowledgement", guard.RecordAttempt(t.Context(), "session", fmt.Sprint(n), "update_progress", args, nil, false))
	}
	for _, tc := range []struct {
		handle  string
		allowed bool
	}{{"fixer#1", false}, {"fixer#2", true}} {
		allowed, _, _, err := guard.Check(t.Context(), "session", "next", "update_progress", map[string]any{
			"update":              []any{map[string]any{"id": "p1", "state": "done", "label": "Different label"}},
			"acknowledge_results": []any{tc.handle},
		}, true, nil)
		testutil.FailErr(t, "check finish identity", err)
		if allowed != tc.allowed {
			t.Fatalf("finish identity allowed=%v want=%v", allowed, tc.allowed)
		}
	}
}

func TestHostIssuedSnapshotRecoversAnExhaustedItemNotFoundBucket(t *testing.T) {
	store := progresstest.New(t, "session")
	progresstest.SeedRows(t, store, "session", progress.RowSpec{Label: "Deliver"})
	current, err := store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "observe current snapshot", err)

	badArgs := map[string]any{"update": []any{map[string]any{"id": "p99", "state": "done"}}}
	guard := NewMemoryDoomLoopGuard()
	for n := range DoomLoopMaxAttempts {
		testutil.FailErr(t, "retain actual progress failure", guard.RecordAttempt(
			t.Context(),
			"session",
			fmt.Sprint(n),
			"update_progress",
			badArgs,
			&api.InvocationFailure{
				Code:    "PROGRESS_ITEM_NOT_FOUND",
				Details: map[string]any{"snapshot": current},
			},
			false,
		))
	}
	wrong := map[string]any{"update": []any{map[string]any{"id": "p88", "state": "done"}}}
	allowed, _, _, err := guard.Check(t.Context(), "session", "wrong", "update_progress", wrong, true, nil)
	testutil.FailErr(t, "check unoffered row id", err)
	if allowed {
		t.Fatal("invented row ID cleared host rejection")
	}

	corrected := map[string]any{"update": []any{map[string]any{"id": "p1", "state": "done"}}}
	allowed, count, _, err := guard.Check(t.Context(), "session", "recovered", "update_progress", corrected, true, nil)
	testutil.FailErr(t, "check host-issued remedy", err)
	if !allowed || count != 0 {
		t.Fatalf("trusted remedy remained blocked: %v count=%d", allowed, count)
	}
	m, err := progress.PrepareMutation(corrected)
	testutil.FailErr(t, "prepare corrected update", err)
	result, err := store.Apply(t.Context(), "session", m, progress.ApplyOptions{})
	testutil.FailErr(t, "retry real progress update", err)
	if !result.StateChanged || len(result.ClosedItemIDs) == 0 {
		t.Fatalf("progress retry lost transition: %+v", result)
	}
	if guard.CodeRejectResponses("session", "update_progress", "PROGRESS_ITEM_NOT_FOUND") != DoomLoopMaxCodeRepeats {
		t.Fatal("recovery erased diagnostic history")
	}
}

func TestProgressRecoveryUsesOnlyStructuredHostRemedies(t *testing.T) {
	snap := progress.RequestSnapshot{
		Rows:               []progress.Row{{ID: "p1", State: progress.ProgressStatePending, Label: "Deliver"}},
		UnaccountedResults: []progress.UnaccountedResult{{Handle: "fixer#1"}},
	}
	itemRecovery := progressRecoveryFromFailure(&api.InvocationFailure{
		Code:    "PROGRESS_ITEM_NOT_FOUND",
		Details: map[string]any{"snapshot": snap},
	})
	if !itemRecovery.matches(map[string]any{"update": []any{map[string]any{"id": "p1", "state": "done"}}}) {
		t.Fatal("valid row ID from snapshot rejected")
	}
	if itemRecovery.matches(map[string]any{"update": []any{map[string]any{"id": "p9", "state": "done"}}}) {
		t.Fatal("unknown row ID matched snapshot")
	}

	handleRecovery := progressRecoveryFromFailure(&api.InvocationFailure{
		Code:    "PROGRESS_RESULT_HANDLE_UNKNOWN",
		Details: map[string]any{"snapshot": snap},
	})
	if !handleRecovery.matches(map[string]any{"acknowledge_results": []any{"fixer#1"}}) {
		t.Fatal("valid handle from snapshot rejected")
	}
	if handleRecovery.matches(map[string]any{"acknowledge_results": []any{"fixer#9"}}) {
		t.Fatal("unknown handle matched snapshot")
	}

	for _, failure := range []*api.InvocationFailure{
		nil,
		{Code: "UNRELATED", Details: map[string]any{"snapshot": snap}},
		{Code: "PROGRESS_ITEM_NOT_FOUND", Details: map[string]any{"snapshot": make(chan int)}},
		{Code: "PROGRESS_ITEM_NOT_FOUND", Details: map[string]any{"snapshot": "not a snapshot"}},
		{Code: "PROGRESS_ITEM_NOT_FOUND"},
	} {
		if progressRecoveryFromFailure(failure).matches(map[string]any{"update": []any{map[string]any{"id": "p1", "state": "done"}}}) {
			t.Fatalf("missing snapshot remedy authorized recovery: %+v", failure)
		}
	}
}

func TestProgressPrerequisiteRecoveryUsesCurrentScopedFacts(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		state      progress.PrerequisiteState
		failure    error
		allowed    bool
	}{
		{name: "pending checklist", code: "PROGRESS_MISSING", state: progress.PrerequisiteState{SessionID: "session", RootSessionID: "root", Missing: false, OpenSteps: 1}, allowed: true},
		{name: "worker exemption", code: "PROGRESS_MISSING", state: progress.PrerequisiteState{SessionID: "session", RootSessionID: "root", Missing: true, WorkerChild: true}, allowed: true},
		{name: "review exemption", code: "PROGRESS_MISSING", state: progress.PrerequisiteState{SessionID: "session", RootSessionID: "root", Missing: true, ReviewLoopActive: true}, allowed: true},
		{name: "still missing", code: "PROGRESS_MISSING", state: progress.PrerequisiteState{SessionID: "session", RootSessionID: "root", Missing: true}},
		{name: "acknowledged finish", code: "PROGRESS_RECONCILIATION_REQUIRED", state: progress.PrerequisiteState{SessionID: "session", RootSessionID: "root"}, allowed: true},
		{name: "pending finish", code: "PROGRESS_RECONCILIATION_REQUIRED", state: progress.PrerequisiteState{SessionID: "session", RootSessionID: "root", Reconciliation: &progress.Reconciliation{Unaccounted: []progress.UnaccountedResult{{Handle: "fixer#1"}}}}},
		{name: "worker finish exemption", code: "PROGRESS_RECONCILIATION_REQUIRED", state: progress.PrerequisiteState{SessionID: "session", RootSessionID: "root", WorkerChild: true, Reconciliation: &progress.Reconciliation{Unaccounted: []progress.UnaccountedResult{{Handle: "fixer#1"}}}}, allowed: true},
		{name: "read failed", code: "PROGRESS_MISSING", failure: errors.New("snapshot unavailable")},
		{name: "wrong session", code: "PROGRESS_MISSING", state: progress.PrerequisiteState{SessionID: "other", RootSessionID: "root", OpenSteps: 1}},
		{name: "missing root", code: "PROGRESS_MISSING", state: progress.PrerequisiteState{SessionID: "session", OpenSteps: 1}},
		{name: "unrelated code", code: "WRITE_SCOPE_DENIED", failure: errors.New("must not load unrelated state")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard := NewMemoryDoomLoopGuard()
			queried := false
			guard.SetProgressPrerequisiteResolver(func(context.Context, string) (progress.PrerequisiteState, error) {
				queried = true
				return tc.state, tc.failure
			})
			args := map[string]any{"path": "main.go"}
			for n := range DoomLoopMaxSameCodeRejects {
				testutil.FailErr(t, "record structured prerequisite refusal", guard.RecordAttempt(t.Context(), "session", fmt.Sprint(n), "edit", args, &api.InvocationFailure{Code: tc.code}, false))
			}
			allowed, count, _, err := guard.Check(t.Context(), "session", "next", "edit", args, true, nil)
			if allowed != tc.allowed {
				t.Fatalf("current prerequisite allowed=%v want=%v err=%v", allowed, tc.allowed, err)
			}
			if tc.code == "WRITE_SCOPE_DENIED" {
				if queried || err != nil {
					t.Fatal("unrelated rejection consulted progress state")
				}
			} else if !queried {
				t.Fatal("current prerequisite was not queried")
			}
			if tc.failure != nil && tc.code != "WRITE_SCOPE_DENIED" && !errors.Is(err, tc.failure) {
				t.Fatalf("read failure was not preserved: %v", err)
			}
			if tc.allowed && count != 0 {
				t.Fatalf("resolved count=%d", count)
			}
		})
	}
}
