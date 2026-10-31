package reporting_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/reporting"
)

func TestUpdateProgress_rejectsTooManyLines(t *testing.T) {
	assertUpdateProgressReject(t,
		"PROGRESS_TOO_MANY_LINES",
		"## Progress\n"+strings.Repeat("- [ ] step\n", progress.MaxAuthorProgressLines+1),
	)
}

func TestUpdateProgress_rejectsNestedLine(t *testing.T) {
	assertUpdateProgressReject(t,
		"PROGRESS_NESTED_LINE",
		"## Progress\n- [ ] top\n  - [ ] nested\n",
	)
}

func TestUpdateProgress_rejectsPendingAfterOptional(t *testing.T) {
	assertUpdateProgressReject(t,
		"PROGRESS_PENDING_AFTER_OPTIONAL",
		"## Progress\n- [>] Synthesize report\n- [ ] Verify rollout\n",
	)
}

func TestUpdateProgress_rejectsNoOpenStepInitial(t *testing.T) {
	assertUpdateProgressReject(t,
		"PROGRESS_NO_OPEN_STEP",
		"## Progress\n- [x] already finished\n",
	)
}

func TestUpdateProgress_acceptsAllTerminalWhenPrevHasRows(t *testing.T) {
	store := progress.NewMemoryStore()
	reg := tools.NewDefaultRegistry()
	scope := func(_ context.Context, id string) string { return id }
	testutil.FailErr(t, "register update_progress", native.RegisterUpdateProgressTool(reg, store, scope, nil))

	store.Set("sess-1", "## Progress\n- [ ] open step\n")
	observed, err := store.Snapshot(t.Context(), "sess-1")
	testutil.FailErr(t, "observe existing plan", err)
	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	_, err = exec.Invoke(context.Background(), "update_progress", map[string]any{
		"content": "## Progress\n- [x] open step\n", "expected_revision": observed.Revision, "item_ids": observed.ItemIDs,
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-1"},
	})
	testutil.FailErr(t, "update_progress all terminal with prior rows", err)
	if !strings.Contains(store.Get(t.Context(), "sess-1"), "- [x] open step") {
		t.Fatal("expected updated plan to be stored")
	}
}

func TestUpdateProgress_acceptsOverlongLabel(t *testing.T) {
	store := progress.NewMemoryStore()
	reg := tools.NewDefaultRegistry()
	scope := func(_ context.Context, id string) string { return id }
	testutil.FailErr(t, "register update_progress", native.RegisterUpdateProgressTool(reg, store, scope, nil))

	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	long := strings.Repeat("x", progress.MaxLabelRunes+3)
	_, err := exec.Invoke(context.Background(), "update_progress", map[string]any{
		"content": "## Progress\n- [ ] " + long,
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-1"},
	})
	testutil.FailErr(t, "update_progress overlong label", err)
	if store.Get(t.Context(), "sess-1") == "" {
		t.Fatal("expected over-long plan to be stored, not rejected")
	}
}

func TestUpdateProgress_unchangedContentNoops(t *testing.T) {
	store := progress.NewMemoryStore()
	reg := tools.NewDefaultRegistry()
	scope := func(_ context.Context, id string) string { return id }
	testutil.FailErr(t, "register update_progress", native.RegisterUpdateProgressTool(reg, store, scope, nil))

	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	content := "## Progress\n- [ ] Ship feature\n"
	writes := 0
	unregister := progress.RegisterWriteObserver(func(_ context.Context, evt progress.WriteEvent) {
		if evt.SessionID == "sess-1" {
			writes++
		}
	})
	t.Cleanup(unregister)

	first, err := exec.Invoke(context.Background(), "update_progress", map[string]any{"content": content}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-1"},
	})
	testutil.FailErr(t, "update_progress first write", err)
	if !strings.Contains(first, "updated") {
		t.Fatalf("first write status = %q want updated", first)
	}

	observed, err := store.Snapshot(t.Context(), "sess-1")
	testutil.FailErr(t, "observe replay revision", err)
	second, err := exec.Invoke(context.Background(), "update_progress", map[string]any{"content": content, "expected_revision": observed.Revision, "item_ids": observed.ItemIDs}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-1"},
	})
	testutil.FailErr(t, "update_progress replay", err)
	var result map[string]any
	testutil.FailErr(t, "decode replay result", json.Unmarshal([]byte(second), &result))
	if result["status"] != "unchanged" || result["document_changed"] != false || result["state_changed"] != false {
		t.Fatalf("replay must retain its no-op outcome: %v", result)
	}
	if writes != 1 {
		t.Fatalf("write notifications = %d want 1", writes)
	}
	if !strings.Contains(second, "unchanged") {
		t.Fatalf("replay status = %q want unchanged", second)
	}
	if got := store.Get(t.Context(), "sess-1"); strings.TrimSpace(got) != strings.TrimSpace(content) {
		t.Fatalf("store mutated on noop replay: %q", got)
	}
}

func TestUpdateProgress_acceptsValidPlan(t *testing.T) {
	store := progress.NewMemoryStore()
	reg := tools.NewDefaultRegistry()
	scope := func(_ context.Context, id string) string { return id }
	testutil.FailErr(t, "register update_progress", native.RegisterUpdateProgressTool(reg, store, scope, nil))

	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	label := strings.Repeat("x", progress.MaxLabelRunes)
	content := strings.Repeat("- [ ] "+label+"\n", progress.MaxAuthorProgressLines-2) +
		"- [x] done\n- [>] Synthesize report\n"
	_, err := exec.Invoke(context.Background(), "update_progress", map[string]any{
		"content": "## Progress\n" + content,
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess-1"},
	})
	testutil.FailErr(t, "update_progress valid plan", err)
	if !strings.Contains(store.Get(t.Context(), "sess-1"), "- [>] Synthesize report") {
		t.Fatal("expected plan to be stored")
	}
}

func assertUpdateProgressReject(t *testing.T, code, content string) {
	t.Helper()
	store := progress.NewMemoryStore()
	reg := tools.NewDefaultRegistry()
	scope := func(_ context.Context, id string) string { return id }
	testutil.FailErr(t, "register update_progress", native.RegisterUpdateProgressTool(reg, store, scope, nil))

	exec := toolexecution.NewExecutor(nil, reg, "coordinator")

	_, err := exec.Invoke(context.Background(), "update_progress", map[string]any{
		"content": content,
	}, tools.ToolContext{
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
	if store.Get(t.Context(), "sess-1") != "" {
		t.Fatal("rejected write must not land in the progress store")
	}
}

func TestUpdateProgressRequiresObservedRevisionForExistingChecklist(t *testing.T) {
	store := progress.NewMemoryStore()
	testutil.FailErr(t, "seed current checklist", store.Set("session", "- [ ] Preserve"))
	before, err := store.Snapshot(t.Context(), "session")
	testutil.FailErr(t, "observe current checklist", err)
	handler := reporting.ProgressHandler(store, func(_ context.Context, id string) string { return id }, nil)
	_, err = handler(t.Context(), map[string]any{"content": "- [x] Preserve"}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}})
	rejection := toolrejection.AsToolReject(err)
	if rejection == nil || rejection.Code != "PROGRESS_REVISION_REQUIRED" {
		t.Fatalf("missing revision outcome: %v", err)
	}
	snapshot, ok := rejection.Data["snapshot"].(progress.Snapshot)
	if !ok || snapshot.Revision != before.Revision || snapshot.Content != before.Content {
		t.Fatalf("missing recovery snapshot: %+v", rejection.Data)
	}
	if store.Get(t.Context(), "session") != before.Content {
		t.Fatal("blind replacement changed current state")
	}
}

func TestProgressHandlerPublishesAdvancementFromTheCommittedResult(t *testing.T) {
	store := progress.NewMemoryStore()
	handler := reporting.ProgressHandler(store, func(_ context.Context, id string) string { return id }, nil)
	for _, tc := range []struct {
		content  string
		advanced bool
	}{{"- [ ] Deliver", false}, {"- [ ] Deliver", false}, {"- [ ] Deliver revised", false}, {"- [x] Deliver revised", true}} {
		before, err := store.Snapshot(t.Context(), "session")
		testutil.FailErr(t, "observe current checklist", err)
		out := &tools.ToolInvocationOut{}
		args := map[string]any{"content": tc.content, "expected_revision": before.Revision}
		if len(before.ItemIDs) > 0 {
			args["item_ids"] = before.ItemIDs
		}
		body, err := handler(t.Context(), args, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "session"}, Effects: tools.InvocationEffects{Out: out}})
		testutil.FailErr(t, "publish checklist", err)
		var result progress.AuthorUpdateResult
		testutil.FailErr(t, "decode display for comparison", json.Unmarshal([]byte(body), &result))
		if out.Facts.WorkflowAdvanced == nil || *out.Facts.WorkflowAdvanced != tc.advanced || result.AdvancesWorkflow() != tc.advanced {
			t.Fatalf("typed/display result mismatch: %+v / %s", out.Facts, body)
		}
	}
}
