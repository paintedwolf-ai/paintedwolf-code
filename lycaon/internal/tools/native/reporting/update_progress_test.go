package reporting_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
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

func TestUpdateProgress_acceptsOverlongLabel(t *testing.T) {
	store := progress.NewMemoryStore()
	reg := tools.NewDefaultRegistry()
	scope := func(_ context.Context, id string) string { return id }
	testutil.FailErr(t, "register update_progress", native.RegisterUpdateProgressTool(reg, store, scope))

	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	long := strings.Repeat("x", progress.MaxLabelRunes+3)
	_, err := exec.Invoke(context.Background(), "update_progress", map[string]any{
		"content": "## Progress\n- [ ] " + long,
	}, tools.ToolContext{SessionID: "sess-1"})
	testutil.FailErr(t, "update_progress overlong label", err)
	if store.Get(t.Context(), "sess-1") == "" {
		t.Fatal("expected over-long plan to be stored, not rejected")
	}
}

func TestUpdateProgress_unchangedContentNoops(t *testing.T) {
	store := progress.NewMemoryStore()
	reg := tools.NewDefaultRegistry()
	scope := func(_ context.Context, id string) string { return id }
	testutil.FailErr(t, "register update_progress", native.RegisterUpdateProgressTool(reg, store, scope))

	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	content := "## Progress\n- [ ] Ship feature\n"

	first, err := exec.Invoke(context.Background(), "update_progress", map[string]any{"content": content}, tools.ToolContext{SessionID: "sess-1"})
	testutil.FailErr(t, "update_progress first write", err)
	if !strings.Contains(first, "updated") {
		t.Fatalf("first write status = %q want updated", first)
	}

	second, err := exec.Invoke(context.Background(), "update_progress", map[string]any{"content": content}, tools.ToolContext{SessionID: "sess-1"})
	testutil.FailErr(t, "update_progress replay", err)
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
	testutil.FailErr(t, "register update_progress", native.RegisterUpdateProgressTool(reg, store, scope))

	exec := toolexecution.NewExecutor(nil, reg, "coordinator")
	label := strings.Repeat("x", progress.MaxLabelRunes)
	content := strings.Repeat("- [ ] "+label+"\n", progress.MaxAuthorProgressLines-2) +
		"- [x] done\n- [>] Synthesize report\n"
	_, err := exec.Invoke(context.Background(), "update_progress", map[string]any{
		"content": "## Progress\n" + content,
	}, tools.ToolContext{SessionID: "sess-1"})
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
	testutil.FailErr(t, "register update_progress", native.RegisterUpdateProgressTool(reg, store, scope))

	exec := toolexecution.NewExecutor(nil, reg, "coordinator")

	_, err := exec.Invoke(context.Background(), "update_progress", map[string]any{
		"content": content,
	}, tools.ToolContext{SessionID: "sess-1"})
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
