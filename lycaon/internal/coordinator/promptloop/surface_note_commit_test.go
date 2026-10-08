package promptloop

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"encoding/json"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCommitToolResultWithOptionalNoteAtomicPair(t *testing.T) {
	var batches [][]api.Message
	loop := &PromptLoop{Deps: PromptLoopDeps{
		AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
			batches = append(batches, append([]api.Message(nil), msgs...))
			return nil
		},
	}}
	last := time.Time{}
	toolMsg := api.Message{
		ID: "tool-1", Role: api.MessageRoleTool, Content: `{"status":"noted"}`,
		ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted},
	}
	stampCommitOrderTS(&toolMsg, &last)
	note := &tools.AgentNoteCapture{
		MessageID: "note-1",
		Content:   "Grounded fact.",
		Grounding: &api.CitationGrounding{Traced: true},
	}
	history, err := toolInvocations{loop}.commitToolResultWithOptionalNote(context.Background(), "sess", nil, toolMsg, nil, note, &last, nil)
	testutil.FailErr(t, "commit", err)
	if len(batches) != 1 || len(batches[0]) != 2 {
		t.Fatalf("batches = %+v want one pair", batches)
	}
	if batches[0][0].ID != "tool-1" || batches[0][1].ID != "note-1" {
		t.Fatalf("order = %q then %q", batches[0][0].ID, batches[0][1].ID)
	}
	if batches[0][1].Kind != api.MessageKindAgentNote {
		t.Fatalf("kind = %q", batches[0][1].Kind)
	}
	if batches[0][0].ToolResult == nil || batches[0][0].ToolResult.UiVisibility != api.ToolResultUiVisibilityBenign {
		t.Fatalf("transport visibility = %+v want benign", batches[0][0].ToolResult)
	}
	if len(history) != 2 || history[0].ID != "tool-1" || history[1].ID != "note-1" {
		t.Fatalf("history = %+v", history)
	}
	if !history[1].CreatedAt.After(history[0].CreatedAt) {
		t.Fatalf("note ts %v must follow tool ts %v", history[1].CreatedAt, history[0].CreatedAt)
	}
}

func TestCommitToolResultWithOptionalNoteAppendFailureCommitsNeither(t *testing.T) {
	loop := &PromptLoop{Deps: PromptLoopDeps{
		AppendMessages: func(_ context.Context, _ string, _ ...api.Message) error {
			return errors.New("store down")
		},
	}}
	last := time.Time{}
	toolMsg := api.Message{ID: "tool-1", Role: api.MessageRoleTool, Content: "ok"}
	stampCommitOrderTS(&toolMsg, &last)
	history, err := toolInvocations{loop}.commitToolResultWithOptionalNote(context.Background(), "sess", []api.Message{{ID: "prior"}}, toolMsg, nil, &tools.AgentNoteCapture{
		MessageID: "note-1",
		Content:   "note",
	}, &last, nil)
	if err == nil {
		t.Fatal("expected append error")
	}
	if len(history) != 1 || history[0].ID != "prior" {
		t.Fatalf("history must stay unchanged on failure: %+v", history)
	}
}

func TestCommitToolResultNilNoteSingleAppend(t *testing.T) {
	var sizes []int
	loop := &PromptLoop{Deps: PromptLoopDeps{
		AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
			sizes = append(sizes, len(msgs))
			return nil
		},
	}}
	last := time.Time{}
	toolMsg := api.Message{ID: "tool-1", Role: api.MessageRoleTool, Content: "ok"}
	stampCommitOrderTS(&toolMsg, &last)
	history, err := toolInvocations{loop}.commitToolResultWithOptionalNote(context.Background(), "sess", nil, toolMsg, nil, nil, &last, nil)
	testutil.FailErr(t, "commit", err)
	if len(sizes) != 1 || sizes[0] != 1 || len(history) != 1 {
		t.Fatalf("sizes=%v history=%+v", sizes, history)
	}
}

func TestExecuteOneToolCallDiscardsNoteOnEmitReject(t *testing.T) {
	reg := tools.NewStubRegistry()
	_ = reg.Register("surface_note", func(_ context.Context, _ map[string]any, tctx tools.ToolContext) (string, error) {
		if tctx.Out != nil {
			tctx.Out.AgentNote = &tools.AgentNoteCapture{
				MessageID: "note-should-drop",
				Content:   "should not land",
				Grounding: &api.CitationGrounding{Traced: true},
			}
		}
		return strings.Repeat("y", tooloutput.DefaultMaxSpillFileBytes+1), nil
	})
	loop := NewPromptLoopForTest(PromptLoopDeps{Tools: reg})
	sess := &api.Session{ID: "s1"}
	out := toolBatch{loop}.executeOneToolCall(context.Background(), sess, "s1", "", nil, api.ToolCall{
		ID: "tc1", Name: "surface_note", Args: map[string]any{"summary": "x"},
	}, tools.ToolContext{SessionID: "s1", Agent: "coordinator"}, nil, "", api.CoordinatorRunContext{}, false)
	testutil.FailErr(t, "executeOneToolCall", out.endTurn)
	if out.agentNote != nil {
		t.Fatalf("note capture must be discarded after emit reject: %+v", out.agentNote)
	}
	if out.toolMsg.Role != api.MessageRoleTool {
		t.Fatalf("expected tool reject/error row, got %+v", out.toolMsg)
	}
	if !strings.Contains(out.toolMsg.Content, "TOOL_RESULT_TOO_LARGE") &&
		(out.toolMsg.ToolResult == nil || out.toolMsg.ToolResult.Outcome == api.ToolResultOutcomeCompleted) {
		t.Fatalf("expected oversized reject/error tool row, got content=%q result=%+v", out.toolMsg.Content, out.toolMsg.ToolResult)
	}
}

func TestExecuteOneToolCallDiscardsNoteOnHandlerReject(t *testing.T) {
	reg := tools.NewStubRegistry()
	_ = reg.Register("surface_note", func(_ context.Context, _ map[string]any, tctx tools.ToolContext) (string, error) {
		if tctx.Out != nil {
			tctx.Out.AgentNote = &tools.AgentNoteCapture{
				MessageID: "note-should-drop",
				Content:   "should not land",
			}
		}
		return "", &tools.ToolReject{Code: "SURFACE_NOTE_UNGROUNDED", Data: map[string]any{}}
	})
	loop := NewPromptLoopForTest(PromptLoopDeps{Tools: reg})
	out := toolBatch{loop}.executeOneToolCall(context.Background(), &api.Session{ID: "s1"}, "s1", "", nil, api.ToolCall{
		ID: "tc1", Name: "surface_note", Args: map[string]any{"summary": "x"},
	}, tools.ToolContext{SessionID: "s1", Agent: "coordinator"}, nil, "", api.CoordinatorRunContext{}, false)
	testutil.FailErr(t, "executeOneToolCall", out.endTurn)
	if out.agentNote != nil {
		t.Fatalf("note capture must be discarded on handler reject: %+v", out.agentNote)
	}
}

func TestCommitToolResultWithOptionalNoteRetryYieldsOnePair(t *testing.T) {
	attempts := 0
	var batches [][]api.Message
	loop := &PromptLoop{Deps: PromptLoopDeps{
		AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
			attempts++
			if attempts == 1 {
				return errors.New("transient store fault")
			}
			batches = append(batches, append([]api.Message(nil), msgs...))
			return nil
		},
	}}
	note := &tools.AgentNoteCapture{
		MessageID: "note-1",
		Content:   "Grounded fact.",
		Grounding: &api.CitationGrounding{Traced: true},
	}
	last := time.Time{}
	toolMsg := api.Message{ID: "tool-1", Role: api.MessageRoleTool, Content: `{"status":"noted"}`}
	stampCommitOrderTS(&toolMsg, &last)
	history, err := toolInvocations{loop}.commitToolResultWithOptionalNote(context.Background(), "sess", nil, toolMsg, nil, note, &last, nil)
	if err == nil {
		t.Fatal("first attempt must fail")
	}
	if len(history) != 0 {
		t.Fatalf("history leaked on fault: %+v", history)
	}
	last = time.Time{}
	toolMsg = api.Message{ID: "tool-1", Role: api.MessageRoleTool, Content: `{"status":"noted"}`}
	stampCommitOrderTS(&toolMsg, &last)
	history, err = toolInvocations{loop}.commitToolResultWithOptionalNote(context.Background(), "sess", nil, toolMsg, nil, note, &last, nil)
	testutil.FailErr(t, "retry commit", err)
	if len(batches) != 1 || len(batches[0]) != 2 {
		t.Fatalf("retry batches = %+v want one pair", batches)
	}
	if len(history) != 2 || history[0].ID != "tool-1" || history[1].ID != "note-1" {
		t.Fatalf("retry history = %+v", history)
	}
}

func TestSurfaceNoteThenSiblingToolContinues(t *testing.T) {
	reg := tools.NewStubRegistry()
	_ = reg.Register("surface_note", func(_ context.Context, _ map[string]any, tctx tools.ToolContext) (string, error) {
		if tctx.Out != nil {
			tctx.Out.AgentNote = &tools.AgentNoteCapture{
				MessageID: "note-1",
				Content:   "Auth lives in middleware.go.",
				Grounding: &api.CitationGrounding{Traced: true},
			}
		}
		return `{"status":"noted","message_id":"note-1"}`, nil
	})
	// Both calls run serially in one batch.
	_ = reg.Register("task", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		return `{"job_id":"job-1","status":"enqueued"}`, nil
	})
	var appended []api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Tools: reg,
		AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
			appended = append(appended, msgs...)
			return nil
		},
	})
	sess := &api.Session{ID: "sess-note", Posture: api.SessionPostureBuild}
	assistantID := "asst-1"
	calls := []api.ToolCall{
		{ID: "tc-note", Name: "surface_note", Args: map[string]any{"summary": "Auth lives in middleware.go."}},
		{ID: "tc-task", Name: "task", Args: map[string]any{
			"agent_type": "implementer",
			"brief":      map[string]any{"goal": "continue", "done_when": []any{"done"}},
			"scope":      map[string]any{"paths": []any{"src/**"}},
		}},
	}
	history := []api.Message{{
		ID: assistantID, Role: api.MessageRoleAssistant, ToolCalls: calls,
	}}
	history, turnTools, _, _, _, breakLoop, err := toolBatch{loop}.executeToolCallsInTurn(
		context.Background(),
		sess,
		sess.ID,
		calls,
		tools.ToolContext{SessionID: sess.ID, Agent: "coordinator"},
		history,
		"investigate",
		assistantID,
		"",
		nil,
	)
	testutil.FailErr(t, "executeToolCallsInTurn", err)

	if breakLoop {
		t.Fatal("surface_note must not end the tool batch")
	}
	if len(turnTools) < 2 {
		t.Fatalf("turnTools = %v want surface_note then task (history=%d appended=%d)", turnTools, len(history), len(appended))
	}
	var noteRows int
	for _, msg := range appended {
		if msg.Kind == api.MessageKindAgentNote {
			noteRows++
		}
	}
	if noteRows != 1 {
		t.Fatalf("note rows = %d want 1; appended=%+v", noteRows, appended)
	}
}

func TestOps8ToolResultPolicyControlsEveryDeliveredCapture(t *testing.T) {
	for _, block := range []bool{false, true} {
		name := "transform"
		if block {
			name = "block"
		}
		t.Run(name, func(t *testing.T) {
			reg := tools.NewStubRegistry()
			testutil.FailErr(t, "register captured tool", reg.Register("surface_note", func(_ context.Context, _ map[string]any, tc tools.ToolContext) (string, error) {
				tc.Out.AgentNote = &tools.AgentNoteCapture{MessageID: "note", Content: "unreviewed-secret"}
				return "unreviewed-secret", nil
			}))
			seen := 0
			loop := NewPromptLoopForTest(PromptLoopDeps{
				Tools: reg,
				EvaluateContentAnchor: func(_ context.Context, _ *api.Session, anchor string, segments []oar.ContentSegment, tool string, args map[string]any) (*guidance.Refusal, bool, string, bool) {
					seen++
					if anchor != oar.AnchorContentToolResult || tool != "surface_note" || args["summary"] != "observed-input" || len(segments) != 1 || !strings.Contains(segments[0].Content, "unreviewed-secret") {
						t.Fatalf("[OAR-FACT-21] incomplete result occurrence: anchor=%s tool=%s args=%v segments=%v", anchor, tool, args, segments)
					}
					if block {
						return guidance.NewRefusal("RESULT_POLICY", "Result withheld"), true, "", false
					}
					return nil, false, "reviewed replacement", true
				},
			})
			out := toolBatch{loop}.executeOneToolCall(t.Context(), &api.Session{ID: "session"}, "session", "", nil,
				api.ToolCall{ID: "call", Name: "surface_note", Args: map[string]any{"summary": "observed-input"}},
				tools.ToolContext{SessionID: "session", Agent: "coordinator"}, nil, "", api.CoordinatorRunContext{}, false)
			testutil.FailErr(t, "execute tool delivery", out.endTurn)
			encoded, err := json.Marshal(out.toolMsg)
			testutil.FailErr(t, "encode delivered result", err)
			if seen != 1 || out.agentNote != nil || strings.Contains(string(encoded), "unreviewed-secret") {
				t.Fatalf("[OAR-OPS-8] source escaped result policy: seen=%d note=%v result=%s", seen, out.agentNote, encoded)
			}
			if !block && !strings.Contains(out.toolMsg.Content, "reviewed replacement") {
				t.Fatalf("[OAR-OPS-8] replacement was not delivered: %s", encoded)
			}
		})
	}
}
