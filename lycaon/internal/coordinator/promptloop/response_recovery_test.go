package promptloop

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type recoverySequence struct {
	modelcall.LLMClient
	failures      []error
	requests      []modelcall.CompletionRequest
	completedCall bool
}

func (c *recoverySequence) Stream(_ context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	index := len(c.requests)
	c.requests = append(c.requests, req)
	ch := make(chan modelcall.StreamChunk, 1)
	if c.completedCall && index == 0 {
		ch <- modelcall.StreamChunk{ToolCalls: []api.ToolCall{{ID: "completed", Name: "read", Args: map[string]any{}}}, Done: true}
		close(ch)
		return ch, nil
	}
	if c.completedCall {
		index--
	}
	if index < len(c.failures) {
		ch <- modelcall.StreamChunk{Content: "rejected draft", ToolCalls: []api.ToolCall{{ID: "never", Name: "read"}}, Err: c.failures[index], Done: true}
	} else {
		ch <- modelcall.StreamChunk{Content: "done", Done: true}
	}
	close(ch)
	return ch, nil
}

func TestLoopRepairsMixedResponseFailuresWithoutExecutingRejectedCalls(t *testing.T) {
	pipeline := outputPolicyPipeline(t)
	malformed := &failure.ProviderOutputProtocolError{Kind: failure.MalformedEnvelope, ProviderID: "p", Model: "m"}
	dropped := &failure.ProviderToolCallUnparsedError{ProviderID: "p", Model: "m"}
	for _, tc := range []struct {
		name          string
		failures      []error
		wantErr       bool
		completedCall bool
		noTools       bool
		schema        bool
	}{
		{"protocol", []error{malformed}, false, false, false, false},
		{"mixed", []error{malformed, dropped}, false, false, false, false},
		{"exhausted", []error{malformed, dropped, malformed}, true, false, false, false},
		{"committed call", []error{malformed}, false, true, false, false},
		{"no tools", []error{malformed}, false, false, true, false},
		{"structured", []error{malformed}, false, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := tools.NewStubRegistry()
			executed := 0
			testutil.FailErr(t, "register", reg.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				executed++
				return "body", nil
			}))
			client := &recoverySequence{failures: tc.failures, completedCall: tc.completedCall}
			var codes []string
			loop := NewPromptLoopForTest(PromptLoopDeps{
				Context: ContextDeps{Tools: reg, Limits: func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() }, BuildMessages: func(_ context.Context, _ *api.Session, h []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
					return h, nil
				}},
				Model: ModelDeps{LLM: client, FitRequest: func(_ context.Context, _ *api.Session, _ []api.Message, req modelcall.CompletionRequest) (modelcall.CompletionRequest, error) {
					req.Model = "selected-model"
					if tc.noTools {
						req.Tools = nil
						req.ToolUse = modelcall.ToolUseForbidden
					}
					if tc.schema {
						req.ResponseFormat = &modelcall.ResponseFormat{Type: modelcall.ResponseFormatJSONSchema, Name: "answer", Schema: []byte(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)}
					}
					return req, nil
				}}, Closeout: CloseoutDeps{EvaluateContentAnchor: outputPolicyEvaluator(t, pipeline())},
				Projection: ProjectionDeps{AppendDraftVersion: func(_ context.Context, _, _, body, code string) (int, error) {
					if body != "rejected draft" {
						t.Fatalf("draft=%q", body)
					}
					codes = append(codes, code)
					return len(codes), nil
				}, AppendMessages: func(context.Context, string, ...api.Message) error { return nil }, UpdateMessage: func(context.Context, string, string, api.Message) error { return nil }},
			})
			sess := &api.Session{ID: t.Name(), Posture: api.SessionPostureBuild}
			history := []api.Message{{Role: api.MessageRoleUser, Content: "Inspect"}, {Role: api.MessageRoleAssistant, Content: "Prior completed work"}}
			result, err := loop.Run(t.Context(), PromptRunInput{SessionID: sess.ID, Session: sess, History: history, ProfileID: "explore_readonly", ToolCtx: tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID}}})
			if tc.wantErr {
				if !errors.Is(err, malformed) {
					t.Fatalf("error=%v", err)
				}
			} else {
				testutil.FailErr(t, "recovery", err)
				if result.LastAssistantContent != "done" {
					t.Fatalf("result=%+v", result)
				}
			}
			if executed != boolInt(tc.completedCall) {
				t.Fatalf("rejected calls executed %d", executed)
			}
			if len(codes) != min(len(tc.failures), 2) {
				t.Fatalf("draft codes=%v", codes)
			}
			for i, req := range client.requests {
				found := false
				for _, m := range req.Messages {
					if m.Content == "Prior completed work" {
						found = true
					}
					if m.ID == "never" || (len(m.ToolCalls) > 0 && m.ToolCalls[0].ID != "completed") {
						t.Fatal("rejected call reached retry history")
					}
				}
				if !found {
					t.Fatal("prior work lost")
				}
				if req.Model != "selected-model" || req.Think != client.requests[0].Think || !reflect.DeepEqual(req.ResponseFormat, client.requests[0].ResponseFormat) || !reflect.DeepEqual(req.Tools, client.requests[0].Tools) || req.ToolUse != client.requests[0].ToolUse {
					t.Fatal("request model controls changed")
				}
				if tc.noTools && req.ToolsCallable() {
					t.Fatal("replacement enabled tools")
				}
				if tc.schema && req.ResponseFormat == nil {
					t.Fatal("replacement lost schema")
				}
				if i > boolInt(tc.completedCall) {
					feedback := false
					for _, m := range req.Messages {
						feedback = feedback || strings.Contains(m.Content, "Code: MODEL_RESPONSE_INVALID") || strings.Contains(m.Content, "Code: TOOL_CALL_UNPARSED")
					}
					if !feedback {
						t.Fatal("missing repair feedback")
					}
				}
			}
		})
	}
}

func TestResponseRecoveryPreservesModesAndCancellation(t *testing.T) {
	pipeline := outputPolicyPipeline(t)
	cause := &failure.ProviderOutputProtocolError{Kind: failure.MalformedEnvelope, ProviderID: "same", Model: "same-model"}
	for _, tc := range []struct {
		name      string
		schema    bool
		canceled  bool
		remaining int
	}{
		{"no tools", false, false, 1}, {"schema", true, false, 1}, {"stopped", false, true, 1}, {"final admission", false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			req := modelcall.CompletionRequest{Model: "same-model"}
			if tc.schema {
				req.ResponseFormat = &modelcall.ResponseFormat{}
			}
			st := &promptLoopTurnState{}
			turn := &modelTurn{Closeout: &turnCloseout{Deps: CloseoutDeps{EvaluateContentAnchor: outputPolicyEvaluator(t, pipeline())}}}
			got, err := turn.recoverRejectedResponse(ctx, &api.Session{ID: t.Name()}, st, req, nil, &modelcall.Completion{Content: "partial", ToolCalls: []api.ToolCall{{ID: "pending", Name: "read"}}}, cause, tc.remaining)
			if tc.canceled || tc.remaining == 0 {
				if got != nil || !errors.Is(err, cause) || st.output.recovery.Consecutive() != 0 {
					t.Fatalf("unexpected recovery: %+v %v", got, err)
				}
				return
			}
			testutil.FailErr(t, "recover", err)
			if got.Content != "partial" || len(got.ToolCalls) != 0 || got.ProviderID != "same" || got.Model != "same-model" || st.output.rejection.Code() != "MODEL_RESPONSE_INVALID" {
				t.Fatalf("recovery=%+v", got)
			}
			if req.ToolsCallable() || ((req.ResponseFormat != nil) != tc.schema) {
				t.Fatal("request contract changed")
			}
		})
	}
}
