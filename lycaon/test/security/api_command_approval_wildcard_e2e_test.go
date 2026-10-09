package security

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func writeCommandWildcardApprovals(t *testing.T, dir string) {
	t.Helper()
	overlay := filepath.Join(dir, settingsoverlay.DirName())
	if err := os.MkdirAll(overlay, 0o700); err != nil {
		testutil.FailErr(t, "mkdir overlay", err)
	}
	body := `rules:
  - category: tool
    pattern: command
    effect: allow
  - category: command
    pattern: "docker rm *"
    effect: deny
  - category: command
    pattern: "sort -o*"
    effect: ask
`
	if err := os.WriteFile(filepath.Join(overlay, "approvals.yaml"), []byte(body), 0o600); err != nil {
		testutil.FailErr(t, "write approvals", err)
	}
}

func TestCommandApprovalWildcardDenyE2E(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: "remove stale container",
		ToolCalls: []llm.MockToolCall{{
			ID: "b1", Name: "command", Args: map[string]any{"command": "docker rm stale"},
		}},
		FollowUpText: "removed container",
	}}})
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock))
	ctx := context.Background()
	dir := t.TempDir()
	writeCommandWildcardApprovals(t, dir)

	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	if err := h.Sessions.Manager.Chats.SetAgentType(ctx, sess.ID, "implementer"); err != nil {
		testutil.FailErr(t, "SetAgentType", err)
	}

	if _, err := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "remove stale container"); err != nil {
		testutil.FailErr(t, "Prompt", err)
	}

	kind := wire.CheckpointKindToolApproval
	pending, err := h.Sessions.Checkpoints.ListPending(ctx, sess.ID, &kind)
	testutil.FailErr(t, "ListPending", err)
	if len(pending) != 0 {
		t.Fatalf("denied command wildcard should not create checkpoint; pending=%+v", pending)
	}

	msgs, err := h.Store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	foundBlocked := false
	for _, m := range msgs {
		if m.Role == wire.MessageRoleTool && strings.Contains(m.Content, "Code: APPROVAL_RULE_DENIED") {
			foundBlocked = true
		}
	}
	if !foundBlocked {
		t.Fatalf("expected tool message with approval deny, got %+v", msgs)
	}
}

func TestCommandApprovalWildcardAskSSEMetadataE2E(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: "sort output",
		ToolCalls: []llm.MockToolCall{{
			ID: "b2", Name: "command", Args: map[string]any{"command": "sort -o /tmp/out data.txt"},
		}},
		FollowUpText: "sorted",
	}}})
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock))
	ctx := context.Background()
	dir := t.TempDir()
	writeCommandWildcardApprovals(t, dir)

	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	if err := h.Sessions.Manager.Chats.SetAgentType(ctx, sess.ID, "implementer"); err != nil {
		testutil.FailErr(t, "SetAgentType", err)
	}

	// Checkpoint events use the project ID as their routing key.
	ch, unsub, err := h.MemoryHub().Subscribe(ctx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "Subscribe", err)
	defer unsub()

	done := make(chan error, 1)
	go func() {
		_, err := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "sort output file")
		done <- err
	}()

	var checkpointID string
	testutil.WaitFor(t, 15*time.Second, func() bool {
		kind := wire.CheckpointKindToolApproval
		pending, err := h.Sessions.Checkpoints.ListPending(ctx, sess.ID, &kind)
		if err == nil && len(pending) == 1 {
			checkpointID = pending[0].ID
			return true
		}
		return false
	})

	var sawMatch bool
	testutil.WaitFor(t, 3*time.Second, func() bool {
		select {
		case envelope, ok := <-ch:
			if !ok {
				return false
			}
			if envelope.Topic != wire.EventTopicCheckpoint {
				return false
			}
			var ev wire.CheckpointEvent
			if err := json.Unmarshal(envelope.Data, &ev); err != nil {
				testutil.FailErr(t, "unmarshal checkpoint event", err)
			}
			if ev.ID != checkpointID || ev.ToolApproval == nil {
				return false
			}
			if ev.ToolApproval.Plan.Presentation.Command != "sort -o /tmp/out data.txt" {
				t.Fatalf("command = %q", ev.ToolApproval.Plan.Presentation.Command)
			}
			if !slices.Contains(ev.ToolApproval.Plan.Reasons, "user_rule") {
				t.Fatalf("reasons = %+v", ev.ToolApproval.Plan.Reasons)
			}
			sawMatch = true
			return true
		default:
			return sawMatch
		}
	})
	if !sawMatch {
		t.Fatal("expected checkpoint SSE with command wildcard matched_rule")
	}

	if _, err := h.Sessions.Checkpoints.ResolveCheckpoint(h.OwnerCtx(t, ctx), sess.ID, checkpointID, wire.CheckpointKindToolApproval, &hitl.DecisionResult{Approved: false}, nil); err != nil {
		testutil.FailErr(t, "ResolveCheckpoint", err)
	}
	if err := <-done; err != nil && !strings.Contains(err.Error(), deniedCopy(t)) {
		testutil.FailErr(t, "Prompt after reject", err)
	}
}
