package promptloop

import (
	"context"
	"errors"
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
	failures []error
	requests []modelcall.CompletionRequest
}

func (c *recoverySequence) Stream(_ context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	index := len(c.requests)
	c.requests = append(c.requests, req)
	ch := make(chan modelcall.StreamChunk, 1)
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
		name     string
		failures []error
		wantErr  bool
	}{
		{"protocol", []error{malformed}, false},
		{"mixed", []error{malformed, dropped}, false},
		{"exhausted", []error{malformed, dropped, malformed}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := tools.NewStubRegistry()
			executed := 0
			testutil.FailErr(t, "register", reg.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				executed++
				return "body", nil
			}))
			client := &recoverySequence{failures: tc.failures}
			var codes []string
			loop := NewPromptLoopForTest(PromptLoopDeps{
				Context: ContextDeps{Tools: reg, Limits: func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() }, BuildMessages: func(_ context.Context, _ *api.Session, h []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
					return h, nil
				}},
				Model: ModelDeps{LLM: client}, Closeout: CloseoutDeps{EvaluateContentAnchor: outputPolicyEvaluator(t, pipeline())},
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
			if executed != 0 {
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
					if m.ID == "never" || len(m.ToolCalls) > 0 {
						t.Fatal("rejected call reached retry history")
					}
				}
				if !found {
					t.Fatal("prior work lost")
				}
				if req.Model != client.requests[0].Model || req.Think != client.requests[0].Think {
					t.Fatal("request model controls changed")
				}
				if i > 0 {
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
