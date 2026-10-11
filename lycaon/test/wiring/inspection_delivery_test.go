package wiring

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestInspectionDeliveryDoesNotRequireSelectedCheck(t *testing.T) {
	var calls atomic.Int32
	mock := llm.NewKickDrivenMock(llm.KickDrivenConfig{
		Fallback: func(_ context.Context, _ llm.Snapshot) modelcall.Completion {
			switch calls.Add(1) {
			case 1:
				return modelcall.Completion{ToolCalls: []api.ToolCall{{ID: "offer-write", Name: "request_tools", Args: map[string]any{"need": "write"}}}}
			case 2:
				return modelcall.Completion{ToolCalls: []api.ToolCall{{ID: "edit-guide", Name: "write", Args: map[string]any{"path": "docs/guide.md", "content": "Updated operating instructions.\n"}}}}
			case 3:
				return modelcall.Completion{ToolCalls: []api.ToolCall{{ID: "inspect-guide", Name: "read", Args: map[string]any{"path": "docs/guide.md"}}}}
			case 4:
				return modelcall.Completion{ToolCalls: []api.ToolCall{{ID: "deliver", Name: "update_progress", Args: map[string]any{"content": "## Progress\n- [x] wiring test plan\n"}}}}
			default:
				return modelcall.Completion{Content: `{"synthesis":"Updated the guide and read back the result. No executable checks were needed.","cited_evidence":[{"path":"docs/guide.md","line":1,"excerpt":"Updated operating instructions."}],"cited_urls":[]}`}
			}
		},
	})
	h := BuildForTest(t, WithLLMClient(mock), WithDecider(decide.Absent{}))
	ctx := t.Context()
	cancel := h.StartBackgroundWorkers(t, ctx)
	t.Cleanup(cancel)
	root := t.TempDir()
	testutil.FailErr(t, "create docs", os.MkdirAll(filepath.Join(root, "docs"), 0o700))
	overlay := filepath.Join(root, settingsoverlay.DirName())
	testutil.FailErr(t, "create project overlay", os.MkdirAll(overlay, 0o700))
	testutil.FailErr(t, "allow fixture write", os.WriteFile(filepath.Join(overlay, "approvals.yaml"), []byte("rules:\n  - category: tool\n    pattern: write\n    effect: allow\n"), 0o600))
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, root)
	testutil.FailErr(t, "create session", err)
	h.Sessions.Manager.Verification.SetVerifyConfig(fixedVerifyConfig("project-check"))
	AttachDefaultAmbient(t, h, ctx, sess.ID)
	h.SeedProgress(t, ctx, sess.ID)
	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Update the guide and inspect the resulting text.")
	testutil.FailErr(t, "deliver inspected change", err)
	if calls.Load() > 8 {
		t.Fatalf("inspection delivery spiraled into %d model calls", calls.Load())
	}
	body, err := os.ReadFile(filepath.Join(root, "docs/guide.md"))
	testutil.FailErr(t, "read delivered file", err)
	if string(body) != "Updated operating instructions.\n" {
		t.Fatalf("delivered material = %q", body)
	}
	messages, err := h.Store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read transcript", err)
	completed := false
	for _, msg := range messages {
		for _, call := range msg.ToolCalls {
			if call.Name == "verify" || call.Name == "command" {
				t.Fatalf("unexpected executable validation: %s", call.Name)
			}
		}
		if msg.Role == api.MessageRoleAssistant && msg.Visibility == api.MessageVisibilityTranscript &&
			strings.Contains(msg.Content, "No executable checks were needed") {
			completed = true
		}
	}
	if !completed {
		t.Fatal("delivered work has no user-facing closeout")
	}
}
