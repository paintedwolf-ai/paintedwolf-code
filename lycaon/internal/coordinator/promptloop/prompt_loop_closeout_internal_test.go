package promptloop

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAppendTurnCloseoutNudge(t *testing.T) {
	var appended []api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		TurnCloseoutNudge: func(_ context.Context, _ *api.Session, _ string, reason TurnCloseoutReason, _ string) HostNudge {
			return HostNudge{
				Content:  "This is your final turn — " + TurnCloseoutReasonText(reason) + ". Give the user a closing summary.",
				SignalID: "turn.closeout",
			}
		},
		AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
			appended = append(appended, msgs...)
			return nil
		},
	})
	history, err := loop.appendTurnCloseoutNudge(context.Background(), &api.Session{Posture: api.SessionPostureBuild}, "s1", "coordinator", nil, TurnCloseoutIterationCap, "", nil)
	if err != nil {
		t.Fatalf("appendTurnCloseoutNudge: %v", err)
	}
	if len(history) != 1 || len(appended) != 1 {
		t.Fatalf("want one closeout nudge, history=%d appended=%d", len(history), len(appended))
	}
	nudge := history[0]
	if nudge.Role != api.MessageRoleUser || nudge.Visibility != api.MessageVisibilityInternal {
		t.Fatalf("closeout nudge must be an internal user message, got role=%s vis=%s", nudge.Role, nudge.Visibility)
	}
	if nudge.Kind != api.MessageKindIterationCapCloseout {
		t.Fatalf("closeout nudge kind = %q", nudge.Kind)
	}
	if !strings.Contains(nudge.Content, "final turn") || !strings.Contains(nudge.Content, "closing summary") {
		t.Fatalf("closeout copy missing final-turn/summary cue: %q", nudge.Content)
	}
}

func TestAppendTurnCloseoutNudgeDedupsVerbatim(t *testing.T) {
	var appended []api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		TurnCloseoutNudge: func(_ context.Context, _ *api.Session, _ string, reason TurnCloseoutReason, _ string) HostNudge {
			return HostNudge{
				Content:  "This is your final turn — " + TurnCloseoutReasonText(reason) + ". Give the user a closing summary.",
				SignalID: "turn.closeout",
			}
		},
		AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
			appended = append(appended, msgs...)
			return nil
		},
	})
	sess := &api.Session{Posture: api.SessionPostureBuild}
	history, err := loop.appendTurnCloseoutNudge(context.Background(), sess, "s1", "coordinator", nil, TurnCloseoutIterationCap, "", nil)
	if err != nil {
		t.Fatalf("first closeout: %v", err)
	}
	// Insert a distinct internal nudge.
	history = append(history, api.Message{Role: api.MessageRoleUser, Visibility: api.MessageVisibilityInternal, Content: "Rejected: write files to disk"})
	// Repeated closeout copy stays deduplicated.
	history, err = loop.appendTurnCloseoutNudge(context.Background(), sess, "s1", "coordinator", history, TurnCloseoutIterationCap, "", nil)
	if err != nil {
		t.Fatalf("second closeout: %v", err)
	}
	closeouts := 0
	for _, m := range history {
		if strings.Contains(m.Content, "final turn") {
			closeouts++
		}
	}
	if closeouts != 1 {
		t.Fatalf("want exactly one closeout in history, got %d", closeouts)
	}
	if len(appended) != 1 {
		t.Fatalf("want closeout persisted once, got %d", len(appended))
	}
}

func TestEarlyWorkerCloseoutExecutesCompleteLeg(t *testing.T) {
	memory := store.NewMemory()
	sess, err := memory.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "project-1")
	testutil.FailErr(t, "create worker", err)
	sess.ParentSessionID = "parent-1"
	sess.AgentType = "security-reviewer"

	completeCalls := 0
	reg := tools.NewStubRegistry()
	testutil.FailErr(t, "register complete_leg", reg.Register("complete_leg", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		completeCalls++
		return `{"recorded":true}`, nil
	}))
	client := llm.NewMockProvider(&llm.MockConfig{
		Responses: []llm.MockResponseEntry{{
			Pattern: ".*", ToolCalls: []llm.MockToolCall{{
				ID: "complete-1", Name: "complete_leg",
				Args: map[string]any{"leg_status": "complete", "brief": "reviewed"},
			}},
		}},
	})
	deps := StoreDeps(memory)
	deps.LLM = client
	deps.Tools = reg
	deps.Policy = &recordingToolPolicy{}
	deps.TurnCloseoutNudge = func(context.Context, *api.Session, string, TurnCloseoutReason, string) HostNudge {
		return HostNudge{Content: "close out with complete_leg", SignalID: "turn.closeout"}
	}
	loop := NewPromptLoopForTest(deps)
	history := []api.Message{{ID: "user-1", Role: api.MessageRoleUser, Content: "finish"}}
	state := &promptLoopTurnState{history: history}
	var assistantID string
	history, assistantID, _, err = loop.runEarlyTurnCloseout(
		t.Context(), sess, sess.ID, "security-reviewer", "finish", history, 1,
		TurnCloseoutIterationCap, "", false,
		PromptRunInput{ToolCtx: tools.ToolContext{SessionID: sess.ID, Agent: sess.AgentType}}, state,
	)
	testutil.FailErr(t, "run early closeout", err)
	if completeCalls != 1 {
		t.Fatalf("complete_leg calls = %d want 1", completeCalls)
	}
	if !workerCompletionAccepted(history, assistantID) {
		t.Fatal("accepted complete_leg receipt missing from early closeout history")
	}
}
