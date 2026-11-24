package reporting_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/progress/progresstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/reporting"
)

func TestUpdateProgress_rejectsTooManyRows(t *testing.T) {
	rows := make([]any, progress.MaxAuthorProgressRows+1)
	for i := range rows {
		rows[i] = map[string]any{"label": "step"}
	}
	assertUpdateProgressReject(t, "PROGRESS_TOO_MANY_ROWS", map[string]any{"replace": rows})
}

func TestUpdateProgress_rejectsPendingAfterOptional(t *testing.T) {
	assertUpdateProgressReject(t, "PROGRESS_PENDING_AFTER_OPTIONAL", map[string]any{
		"replace": []any{
			map[string]any{"label": "Synthesize report", "state": "optional"},
			map[string]any{"label": "Verify rollout", "state": "pending"},
		},
	})
}

func TestUpdateProgress_rejectsNoOpenStepInitial(t *testing.T) {
	assertUpdateProgressReject(t, "PROGRESS_NO_OPEN_STEP", map[string]any{
		"replace": []any{
			map[string]any{"label": "already finished", "state": "done"},
		},
	})
}

func TestUpdateProgress_rejectsItemNotFound(t *testing.T) {
	assertUpdateProgressReject(t, "PROGRESS_ITEM_NOT_FOUND", map[string]any{
		"update": []any{map[string]any{"id": "p99", "state": "done"}},
	})
}

func TestUpdateProgress_rejectsConflictingOperations(t *testing.T) {
	assertUpdateProgressReject(t, "PROGRESS_CONFLICTING_OPERATIONS", map[string]any{
		"replace": []any{map[string]any{"label": "Plan"}},
		"add":     []any{map[string]any{"label": "Extra"}},
	})
}

func TestUpdateProgress_acceptsAllTerminalWhenPrevHasRows(t *testing.T) {
	store := progresstest.New(t, "sess-1")
	reg := tools.NewDefaultRegistry()
	scope := func(_ context.Context, id string) string { return id }
	testutil.FailErr(t, "register update_progress", native.RegisterUpdateProgressTool(reg, store, scope, nil))

	progresstest.SeedRows(t, store, "sess-1", progress.RowSpec{Label: "open step"})
	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	_, err := exec.Invoke(context.Background(), "update_progress", map[string]any{
		"update": []any{map[string]any{"id": "p1", "state": "done"}},
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-1"},
	})
	testutil.FailErr(t, "update_progress all terminal with prior rows", err)
	snap, err := store.Snapshot(t.Context(), "sess-1")
	testutil.FailErr(t, "snapshot", err)
	if len(snap.Rows) != 1 || snap.Rows[0].State != progress.ProgressStateDone {
		t.Fatalf("expected closed row, got %+v", snap.Rows)
	}
}

func TestUpdateProgress_clampsOverlongLabel(t *testing.T) {
	store := progresstest.New(t, "sess-1")
	reg := tools.NewDefaultRegistry()
	scope := func(_ context.Context, id string) string { return id }
	testutil.FailErr(t, "register update_progress", native.RegisterUpdateProgressTool(reg, store, scope, nil))

	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	displayLong := strings.Repeat("x", progress.MaxLabelRunes+10)
	_, err := exec.Invoke(context.Background(), "update_progress", map[string]any{
		"replace": []any{map[string]any{"label": displayLong}},
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-1"},
	})
	testutil.FailErr(t, "update_progress display-clamped label", err)
	snap, err := store.Snapshot(t.Context(), "sess-1")
	testutil.FailErr(t, "snapshot", err)
	if len(snap.Rows) != 1 || snap.Rows[0].Label != displayLong {
		t.Fatalf("expected stored label preserved up to 240 runes, got %+v", snap.Rows)
	}
	wantClamped := strings.Repeat("x", progress.MaxLabelRunes) + "…"
	if steps := snap.VisibleSteps(); len(steps) != 1 || steps[0].Label != wantClamped {
		t.Fatalf("expected visible step %q, got %+v", wantClamped, steps)
	}
	overCap := strings.Repeat("x", progress.MaxAuthorLabelRunes+1)
	if _, err := exec.Invoke(context.Background(), "update_progress", map[string]any{
		"replace": []any{map[string]any{"label": overCap}},
	}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "sess-1"}}); err == nil || !strings.Contains(err.Error(), "TOOL_ARGS_INVALID") {
		t.Fatalf("expected TOOL_ARGS_INVALID above %d runes, got %v", progress.MaxAuthorLabelRunes, err)
	}
}

func TestUpdateProgress_unchangedUpdateNoops(t *testing.T) {
	store := progresstest.New(t, "sess-1")
	reg := tools.NewDefaultRegistry()
	scope := func(_ context.Context, id string) string { return id }
	testutil.FailErr(t, "register update_progress", native.RegisterUpdateProgressTool(reg, store, scope, nil))

	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	writes := 0
	unregister := progress.RegisterWriteObserver(func(_ context.Context, evt progress.WriteEvent) {
		if evt.SessionID == "sess-1" {
			writes++
		}
	})
	t.Cleanup(unregister)

	first, err := exec.Invoke(context.Background(), "update_progress", map[string]any{
		"replace": []any{map[string]any{"label": "Ship feature"}},
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-1"},
	})
	testutil.FailErr(t, "update_progress first write", err)
	if !strings.Contains(first, `"status":"updated"`) {
		t.Fatalf("first write status = %q want updated", first)
	}

	second, err := exec.Invoke(context.Background(), "update_progress", map[string]any{
		"update": []any{map[string]any{"id": "p1", "state": "pending", "label": "Ship feature"}},
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-1"},
	})
	testutil.FailErr(t, "update_progress noop replay", err)
	var result struct {
		Status          string `json:"status"`
		DocumentChanged bool   `json:"document_changed"`
		StateChanged    bool   `json:"state_changed"`
	}
	testutil.FailErr(t, "decode replay result", json.Unmarshal([]byte(second), &result))
	if result.Status != "unchanged" || result.DocumentChanged || result.StateChanged {
		t.Fatalf("replay must retain its no-op outcome: %+v", result)
	}
	if writes != 1 {
		t.Fatalf("write notifications = %d want 1", writes)
	}
}

func TestUpdateProgress_acceptsValidPlan(t *testing.T) {
	store := progresstest.New(t, "sess-1")
	reg := tools.NewDefaultRegistry()
	scope := func(_ context.Context, id string) string { return id }
	testutil.FailErr(t, "register update_progress", native.RegisterUpdateProgressTool(reg, store, scope, nil))

	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	label := strings.Repeat("x", progress.MaxLabelRunes)
	rows := make([]any, 0, progress.MaxAuthorProgressRows)
	for range progress.MaxAuthorProgressRows - 2 {
		rows = append(rows, map[string]any{"label": label})
	}
	rows = append(rows,
		map[string]any{"label": "done", "state": "done"},
		map[string]any{"label": "Synthesize report", "state": "optional"},
	)
	_, err := exec.Invoke(context.Background(), "update_progress", map[string]any{
		"replace": rows,
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-1"},
	})
	testutil.FailErr(t, "update_progress valid plan", err)
	snap, err := store.Snapshot(t.Context(), "sess-1")
	testutil.FailErr(t, "snapshot", err)
	if len(snap.Rows) != progress.MaxAuthorProgressRows {
		t.Fatalf("stored rows = %d, want %d", len(snap.Rows), progress.MaxAuthorProgressRows)
	}
}

func assertUpdateProgressReject(t *testing.T, code string, args map[string]any) {
	t.Helper()
	store := progresstest.New(t, "sess-1")
	reg := tools.NewDefaultRegistry()
	scope := func(_ context.Context, id string) string { return id }
	testutil.FailErr(t, "register update_progress", native.RegisterUpdateProgressTool(reg, store, scope, nil))

	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	_, err := exec.Invoke(context.Background(), "update_progress", args, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-1"},
	})
	if err == nil {
		t.Fatalf("expected reject code %s", code)
	}
	msg := err.Error()
	if !strings.Contains(msg, code) {
		t.Fatalf("missing %q in reject:\n%s", code, msg)
	}
	if toolrejection.AsToolReject(err) == nil {
		t.Fatalf("expected ToolReject observation for %s, got:\n%s", code, msg)
	}
	snap, snapErr := store.Snapshot(t.Context(), "sess-1")
	testutil.FailErr(t, "snapshot", snapErr)
	if len(snap.Rows) != 0 {
		t.Fatal("rejected write must not land in the progress store")
	}
}

func TestProgressHandlerPublishesAdvancementFromTheCommittedResult(t *testing.T) {
	store := progresstest.New(t, "session")
	handler := reporting.ProgressHandler(store, func(_ context.Context, id string) string { return id }, nil)
	for _, tc := range []struct {
		args     map[string]any
		advanced bool
	}{
		{map[string]any{"replace": []any{map[string]any{"label": "Deliver"}}}, false},
		{map[string]any{"update": []any{map[string]any{"id": "p1", "label": "Deliver"}}}, false},
		{map[string]any{"update": []any{map[string]any{"id": "p1", "label": "Deliver revised"}}}, false},
		{map[string]any{"update": []any{map[string]any{"id": "p1", "state": "done"}}}, true},
	} {
		out := &tools.ToolInvocationOut{}
		body, err := handler(t.Context(), tc.args, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "session"},
			Effects:  tools.InvocationEffects{Out: out},
		})
		testutil.FailErr(t, "publish checklist", err)
		var result struct {
			StateChanged        bool     `json:"state_changed"`
			AcknowledgedResults []string `json:"acknowledged_results"`
		}
		testutil.FailErr(t, "decode display for comparison", json.Unmarshal([]byte(body), &result))
		wantAdvanced := result.StateChanged || len(result.AcknowledgedResults) > 0
		if out.Facts.WorkflowAdvanced == nil || *out.Facts.WorkflowAdvanced != tc.advanced || wantAdvanced != tc.advanced {
			t.Fatalf("typed/display result mismatch: %+v / %s", out.Facts, body)
		}
	}
}
