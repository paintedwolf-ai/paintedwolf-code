package wiring

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// commandProgramSession runs one provider-emitted command program through the
// production host and returns the delivered tool result.
type commandProgramSession struct {
	h       *Harness
	dir     string
	session string
}

func newCommandProgramSession(t *testing.T, program, approvals string) *commandProgramSession {
	t.Helper()
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern:      "run the program",
		ToolCalls:    []llm.MockToolCall{{ID: "program-1", Name: "command", Args: map[string]any{"command": program}}},
		FollowUpText: "Program finished.",
	}}})
	h := BuildForTest(t, WithLLMClient(mock))
	dir := t.TempDir()
	testutil.FailErr(t, "seed sub dir", os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	testutil.FailErr(t, "seed notes", os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("first note\nsecond note\n"), 0o644))
	overlay := filepath.Join(dir, settingsoverlay.DirName())
	testutil.FailErr(t, "create overlay", os.MkdirAll(overlay, 0o700))
	rules := "rules:\n  - category: tool\n    pattern: command\n    effect: allow\n  - category: tool\n    pattern: write\n    effect: allow\n" + approvals
	testutil.FailErr(t, "write approvals", os.WriteFile(filepath.Join(overlay, "approvals.yaml"), []byte(rules), 0o600))
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "set agent type", h.Sessions.Manager.Chats.SetAgentType(t.Context(), sess.ID, "implementer"))
	return &commandProgramSession{h: h, dir: dir, session: sess.ID}
}

func (s *commandProgramSession) prompt(ctx context.Context) error {
	_, err := s.h.Sessions.Manager.Submissions.Prompt(ctx, s.session, "run the program")
	return err
}

func (s *commandProgramSession) result(t *testing.T) string {
	t.Helper()
	msgs, err := s.h.Store.GetMessages(t.Context(), s.session)
	testutil.FailErr(t, "read transcript", err)
	// The messages API carries tool output only on the projected tool result.
	for _, raw := range msgs {
		msg := messageview.RedactMessage(raw)
		if msg.Role != wire.MessageRoleTool || msg.ToolResult == nil || msg.ToolResult.ToolCallID != "program-1" {
			continue
		}
		content, _, err := messageview.RetainedContent(raw, "tool_output", "program-1")
		testutil.FailErr(t, "read projected command result", err)
		if !strings.Contains(content, `"program_id"`) {
			t.Fatalf("projected command result carries no program outcome: %q (%+v)", content, msg.ToolResult)
		}
		return content
	}
	t.Fatalf("no projected command result in %d messages", len(msgs))
	return ""
}

// executions lists child receipts as operation=state in launch order.
func (s *commandProgramSession) executions(t *testing.T) []string {
	t.Helper()
	rows, err := s.h.DB.QueryContext(t.Context(), `SELECT e.operation_id, e.state FROM invocation_program_executions e
		JOIN invocation_programs p ON p.id = e.program_id JOIN invocation_receipts r ON r.id = p.parent_receipt_id
		WHERE r.session_id = ? AND e.operation_id NOT LIKE 'program %' ORDER BY e.ordinal`, s.session)
	testutil.FailErr(t, "read executions", err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var operation, state string
		testutil.FailErr(t, "scan execution", rows.Scan(&operation, &state))
		out = append(out, operation+"="+state)
	}
	testutil.FailErr(t, "iterate executions", rows.Err())
	return out
}

func (s *commandProgramSession) programState(t *testing.T) string {
	t.Helper()
	var state string
	testutil.FailErr(t, "read program", s.h.DB.QueryRowContext(t.Context(), `SELECT p.state FROM invocation_programs p JOIN invocation_receipts r ON r.id = p.parent_receipt_id WHERE r.session_id = ?`, s.session).Scan(&state))
	return state
}

func requireExecutions(t *testing.T, got []string, want ...string) {
	t.Helper()
	for _, entry := range want {
		found := false
		for _, row := range got {
			found = found || row == entry
		}
		if !found {
			t.Fatalf("child receipts %v lack %s", got, entry)
		}
	}
}

func TestCommandProgramNativePipelineDeliversThroughProvider(t *testing.T) {
	s := newCommandProgramSession(t, `cat notes.txt | head -n 1; grep -c note notes.txt | tr -d '\n'; printf ' %s\n' "${MISSING:-done}"`, "")
	testutil.FailErr(t, "prompt", s.prompt(t.Context()))
	if result := s.result(t); !strings.Contains(result, "first note") || strings.Contains(result, "second note") || !strings.Contains(result, `2 done`) {
		t.Fatalf("delivered result = %q", result)
	}
	requireExecutions(t, s.executions(t), "read=completed", "grep=completed")
	if state := s.programState(t); state != "completed" {
		t.Fatalf("program state = %q", state)
	}
}

func TestCommandProgramExternalLaunchDeliversThroughProvider(t *testing.T) {
	s := newCommandProgramSession(t, "go version", "")
	testutil.FailErr(t, "prompt", s.prompt(t.Context()))
	if result := s.result(t); !strings.Contains(result, "go version go") {
		t.Fatalf("delivered result = %q", result)
	}
	requireExecutions(t, s.executions(t), "command=completed")
}

// A redirect binds its target from the statement's directory frame and
// publishes through the native write owner.
func TestCommandProgramRedirectWritesFromStatementDirectory(t *testing.T) {
	s := newCommandProgramSession(t, "cd sub && head -n 1 ../notes.txt > copied.txt", "")
	testutil.FailErr(t, "prompt", s.prompt(t.Context()))
	body, err := os.ReadFile(filepath.Join(s.dir, "sub", "copied.txt"))
	testutil.FailErr(t, "read redirect target", err)
	if string(body) != "first note\n" {
		t.Fatalf("redirect target = %q", body)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "copied.txt")); !os.IsNotExist(err) {
		t.Fatalf("redirect bound from the program root: %v", err)
	}
	requireExecutions(t, s.executions(t), "read=completed")
}

// An external launch that needs approval waits on a real checkpoint and runs
// only after the person approves it.
func TestCommandProgramApprovalRequiredExternalRunsAfterApproval(t *testing.T) {
	s := newCommandProgramSession(t, "go env GOOS", "  - category: command\n    pattern: \"go env*\"\n    effect: ask\n")
	ctx := t.Context()
	done := make(chan error, 1)
	go func() { done <- s.prompt(ctx) }()
	var checkpointID string
	kind := wire.CheckpointKindToolApproval
	testutil.WaitFor(t, 30*time.Second, func() bool {
		pending, err := s.h.Sessions.Checkpoints.ListPending(ctx, s.session, &kind)
		if err == nil && len(pending) == 1 {
			checkpointID = pending[0].ID
			return true
		}
		return false
	})
	_, err := s.h.Sessions.Checkpoints.ResolveCheckpoint(s.h.OwnerCtx(t, ctx), s.session, checkpointID, kind, &hitl.DecisionResult{Approved: true}, nil)
	testutil.FailErr(t, "approve launch", err)
	testutil.FailErr(t, "prompt", <-done)
	if result := s.result(t); !strings.Contains(result, "darwin") && !strings.Contains(result, "linux") {
		t.Fatalf("delivered result = %q", result)
	}
	requireExecutions(t, s.executions(t), "command=completed")
}

// Program output redirected into a credential file reaches the protected
// write review of the native write owner; declining leaves the file intact.
func TestCommandProgramRedirectIntoCredentialFileAsksAsProtectedWrite(t *testing.T) {
	for _, c := range []struct{ name, program, target string }{
		{"stdout", "printf leaked > .env", ".env"},
		{"append", "printf leaked >> .env", ".env"},
		{"pipeline stage", "printf leaked | sort > .env.production", ".env.production"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newCommandProgramSession(t, c.program, "")
			credential := filepath.Join(s.dir, c.target)
			testutil.FailErr(t, "seed credential", os.WriteFile(credential, []byte("TOKEN=SECRET\n"), 0o600))
			ctx := t.Context()
			done := make(chan error, 1)
			go func() { done <- s.prompt(ctx) }()
			kind := wire.CheckpointKindToolApproval
			var checkpoint *wire.CheckpointEvent
			testutil.WaitFor(t, 30*time.Second, func() bool {
				pending, err := s.h.Sessions.Checkpoints.ListPending(ctx, s.session, &kind)
				if err == nil && len(pending) == 1 {
					checkpoint = &pending[0]
					return true
				}
				return false
			})
			if approval := checkpoint.ToolApproval; approval == nil || !slices.Contains(approval.Plan.Reasons, wire.GateSensitiveLocation) {
				t.Fatalf("redirect into %s asked without the sensitive location gate: %+v", c.target, checkpoint)
			}
			_, err := s.h.Sessions.Checkpoints.ResolveCheckpoint(s.h.OwnerCtx(t, ctx), s.session, checkpoint.ID, kind, &hitl.DecisionResult{Approved: false}, nil)
			testutil.FailErr(t, "decline protected write", err)
			<-done
			body, err := os.ReadFile(credential)
			testutil.FailErr(t, "read credential", err)
			if string(body) != "TOKEN=SECRET\n" {
				t.Fatalf("declined review still changed %s: %q", c.target, body)
			}
		})
	}
}

// An invented capability_request key is typed argument feedback, never
// silently dropped.
func TestCommandProgramRefusesUnknownCapabilityRequestKeys(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern:      "run the program",
		ToolCalls:    []llm.MockToolCall{{ID: "program-1", Name: "command", Args: map[string]any{"command": "go version", "capability_request": map[string]any{"network": true}}}},
		FollowUpText: "Program finished.",
	}}})
	h := BuildForTest(t, WithLLMClient(mock))
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, t.TempDir())
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "set agent type", h.Sessions.Manager.Chats.SetAgentType(t.Context(), sess.ID, "implementer"))
	_, err = h.Sessions.Manager.Submissions.Prompt(t.Context(), sess.ID, "run the program")
	testutil.FailErr(t, "prompt", err)
	msgs, err := h.Store.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read transcript", err)
	for _, raw := range msgs {
		content, _, err := messageview.RetainedContent(raw, "tool_output", "program-1")
		if err != nil {
			continue
		}
		if !strings.Contains(content, "TOOL_ARGS_INVALID") || !strings.Contains(content, "network") {
			t.Fatalf("unknown capability key result: %s", content)
		}
		return
	}
	t.Fatal("no command result delivered")
}
