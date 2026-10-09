package security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestDirectAgentsMDWriteRequiresApproval(t *testing.T) {
	for _, fixture := range []struct {
		tool string
		args map[string]any
		kind wire.CheckpointKind
	}{
		{"write", map[string]any{"path": "AGENTS.md", "content": "# Agent draft\n"}, wire.CheckpointKindContentApply},
		{"copy", map[string]any{"copies": []any{map[string]any{"from": "draft.md", "to": "AGENTS.md"}}}, wire.CheckpointKindToolApproval},
	} {
		t.Run(fixture.tool, func(t *testing.T) {
			runAgentsMDWriteApproval(t, llm.MockToolCall{ID: "w1", Name: fixture.tool, Args: fixture.args}, fixture.kind)
		})
	}
}

func runAgentsMDWriteApproval(t *testing.T, call llm.MockToolCall, kind wire.CheckpointKind) {
	t.Helper()
	t.Setenv("LYCAON_LLM_MOCK", "1")
	var calls atomic.Int32
	mock := llm.NewKickDrivenMock(llm.KickDrivenConfig{Fallback: func(context.Context, llm.Snapshot) modelcall.Completion {
		switch calls.Add(1) {
		case 1:
			return modelcall.Completion{ToolCalls: []wire.ToolCall{{ID: "offer-tool", Name: "request_tools", Args: map[string]any{"need": call.Name}}}}
		case 2:
			return modelcall.Completion{ToolCalls: []wire.ToolCall{{ID: call.ID, Name: call.Name, Args: call.Args}}}
		default:
			return modelcall.Completion{Content: "Updated AGENTS.md."}
		}
	}})
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock), wiring.WithDecider(decide.Absent{}))
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	dir := t.TempDir()
	testutil.FailErr(t, "seed draft", os.WriteFile(filepath.Join(dir, "draft.md"), []byte("# Agent draft\n"), 0o644))
	project := createAPIProjectAtPath(t, httpSrv.URL, dir)
	sess := openAPIPostJSON[wire.Session](t, httpSrv.URL, "/v1/sessions", nil,
		`{"project_id":"`+project.ID+`","posture":"build"}`, http.StatusAccepted)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	testutil.FailErr(t, "set implementer profile", h.SessionMgr.SetAgentType(ctx, sess.ID, "implementer"))
	// A checklist lets the write reach the approval gate.
	h.SeedProgress(t, ctx, sess.ID)
	done := make(chan error, 1)
	go func() { _, err := h.SessionMgr.Prompt(ctx, sess.ID, "update agents policy"); done <- err }()
	var checkpoint wire.CheckpointEvent
	testutil.WaitFor(t, 15*time.Second, func() bool {
		pending, err := h.CheckpointMgr.ListPending(ctx, sess.ID, nil)
		if err == nil && len(pending) > 0 {
			checkpoint = pending[0]
			return true
		}
		select {
		case err := <-done:
			messages, readErr := h.Store.GetMessages(ctx, sess.ID)
			testutil.FailErr(t, "read completed session", readErr)
			t.Fatalf("session completed before approval: %v\n%s", err, flattenMessages(messages))
		default:
		}
		return false
	})
	assertAgentsMDReview(t, checkpoint, kind)
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("instructions changed before approval: %v", err)
	}
	approveAgentsMDReview(t, h.OwnerCtx(t, ctx), h.CheckpointMgr, sess.ID, checkpoint)
	testutil.FailErr(t, "complete prompt without another approval", <-done)
	content, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	testutil.FailErr(t, "read approved instructions", err)
	if string(content) != "# Agent draft\n" {
		t.Fatalf("approved content = %q", content)
	}
}

func assertAgentsMDReview(t *testing.T, checkpoint wire.CheckpointEvent, kind wire.CheckpointKind) {
	t.Helper()
	if checkpoint.Kind != kind {
		t.Fatalf("instruction approval kind = %s, want %s", checkpoint.Kind, kind)
	}
	if kind == wire.CheckpointKindContentApply {
		if checkpoint.ContentApply == nil || checkpoint.ContentApply.Path != "AGENTS.md" || checkpoint.ContentApply.After != "# Agent draft\n" {
			t.Fatalf("content review lost its proposal: %+v", checkpoint)
		}
		return
	}
	if checkpoint.ToolApproval == nil {
		t.Fatal("instruction approval has no plan")
	}
	changes := checkpoint.ToolApproval.Plan.Presentation.FileChanges
	if len(changes) != 1 || changes[0].Path != "AGENTS.md" || changes[0].After != "# Agent draft\n" {
		t.Fatalf("instruction approval lost its proposal: %+v", changes)
	}
}

func approveAgentsMDReview(t *testing.T, ctx context.Context, manager hitl.CheckpointManager, sessionID string, checkpoint wire.CheckpointEvent) {
	t.Helper()
	if checkpoint.Kind == wire.CheckpointKindContentApply {
		_, err := manager.ResolveCheckpoint(ctx, sessionID, checkpoint.ID, checkpoint.Kind, nil,
			&hitl.ContentApplyResolve{Decision: wire.ContentApplyApprove})
		testutil.FailErr(t, "approve content review", err)
		return
	}
	resolver, ok := manager.(*hitl.Checkpoints)
	if !ok {
		t.Fatal("checkpoint manager lacks approval resolution")
	}
	_, err := resolver.Authority.ResolveApprovalOption(ctx, sessionID, checkpoint.ID, hitl.CurrentActionOption().ID)
	testutil.FailErr(t, "approve instruction change", err)
}

func TestHumanSourceEditorWritesAgentsMD(t *testing.T) {
	h := wiring.BuildForTest(t)
	httpSrv := httptest.NewServer(h.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL

	dir := t.TempDir()
	agentsPath := filepath.Join(dir, "AGENTS.md")
	testutil.FailErr(t, "seed AGENTS.md", os.WriteFile(agentsPath, []byte("# Baseline policy\n"), 0o644))

	project := createAPIProjectAtPath(t, base, dir)

	read := openAPIGetJSON[wire.ProjectSourceReadResponse](t, base, "/v1/projects/{id}/source?path=AGENTS.md",
		map[string]string{"id": project.ID}, http.StatusOK)
	if read.SHA256 == "" {
		t.Fatal("source read must report a base sha for editor saves")
	}

	const updated = "# Human policy\n\nEdited in the app.\n"
	body := `{"path":"AGENTS.md","content":` + strconv.Quote(updated) + `,"encoding":` +
		strconv.Quote(string(read.Encoding)) + `,"base_sha256":"` + read.SHA256 + `"}`
	openAPIPutJSON[wire.ProjectSourceWriteResponse](t, base, "/v1/projects/{id}/source",
		map[string]string{"id": project.ID}, body, http.StatusOK)

	got, err := os.ReadFile(agentsPath)
	testutil.FailErr(t, "ReadFile", err)
	if string(got) != updated {
		t.Fatalf("disk content = %q want %q", string(got), updated)
	}
}
