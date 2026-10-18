package promptloop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providers/ollama"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCoordinatorLLMLoopProgressGuardedOnAllCoordinatorTurns(t *testing.T) {
	t.Parallel()

	userTurn := coordinatorLLMLoopProgress("coordinator", surface.SurfaceImplementDispatch, "Starting workers", false, 0, 8, false)
	if userTurn == nil || !userTurn.Guarded {
		t.Fatalf("user turn loop = %+v want guarded", userTurn)
	}
	if userTurn.HostTurn {
		t.Fatal("user turn must not set host_turn")
	}
	if userTurn.Iteration != 1 || userTurn.MaxIterations != 8 {
		t.Fatalf("iteration = %d/%d want 1/8 on every coordinator turn", userTurn.Iteration, userTurn.MaxIterations)
	}
	if userTurn.ProseFinish {
		t.Fatal("ordinary user turn must not set prose_finish")
	}

	hostTurn := coordinatorLLMLoopProgress("coordinator", surface.SurfaceImplementDispatch, "Starting workers", true, 2, 8, false)
	if hostTurn == nil || !hostTurn.Guarded {
		t.Fatalf("host turn loop = %+v want guarded", hostTurn)
	}
	if !hostTurn.HostTurn || hostTurn.Iteration != 3 || hostTurn.MaxIterations != 8 {
		t.Fatalf("host turn loop = %+v want host iteration 3/8", hostTurn)
	}

	closeout := coordinatorLLMLoopProgress("coordinator", surface.SurfaceImplementDispatch, "Starting workers", false, 6, 500, true)
	if closeout == nil || !closeout.ProseFinish {
		t.Fatalf("early closeout loop = %+v want prose_finish", closeout)
	}
	if closeout.Iteration != 7 || closeout.MaxIterations != 500 {
		t.Fatalf("early closeout iteration = %d/%d want 7/500", closeout.Iteration, closeout.MaxIterations)
	}

	if loop := coordinatorLLMLoopProgress("implementer", "", "", false, 0, 8, false); loop != nil {
		t.Fatalf("worker profile loop = %+v want nil", loop)
	}
}

const unparsedSyntaxError = "invalid character '$' after object key:value pair"

type unparsedRequestRecorder struct {
	modelcall.LLMClient
	requests []modelcall.CompletionRequest
}

func (r *unparsedRequestRecorder) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	r.requests = append(r.requests, req)
	return r.LLMClient.Stream(ctx, req)
}

// A native call Ollama's parser dropped as invalid JSON reaches the model as
// TOOL_CALL_UNPARSED feedback on the next request; nothing executes, and the
// third consecutive drop fails with the provider's original error.
func TestLoopRecoversOllamaDroppedToolCall(t *testing.T) {
	newPipeline := outputPolicyPipeline(t)
	catalog, err := llm.LoadProviderConfig()
	testutil.FailErr(t, "load provider catalog", err)
	rejections := catalog.ToolCallRejectionProfiles["ollama"]
	for _, tc := range []struct {
		name         string
		beforeOutput bool
		drops        int
		wantErr      bool
	}{
		{name: "after output", drops: 1},
		{name: "before output", beforeOutput: true, drops: 1},
		{name: "bounded", drops: maxToolCallUnparsedRecoveries},
		{name: "exhausted", drops: maxToolCallUnparsedRecoveries + 1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var served atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/chat" {
					http.NotFound(w, r)
					return
				}
				_, _ = io.Copy(io.Discard, r.Body)
				if int(served.Add(1)) <= tc.drops {
					if tc.beforeOutput {
						w.WriteHeader(http.StatusInternalServerError)
					} else {
						_, _ = io.WriteString(w, `{"message":{"content":"Writing the page."},"done":false}`+"\n")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"error": unparsedSyntaxError})
					return
				}
				_, _ = io.WriteString(w, `{"message":{"content":"done"},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":1}`+"\n")
			}))
			defer srv.Close()

			reg := tools.NewStubRegistry()
			executed := 0
			testutil.FailErr(t, "register", reg.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				executed++
				return "file-body", nil
			}))
			client := &unparsedRequestRecorder{LLMClient: ollama.New("desktop", srv.URL+"/v1", "", []modelinfo.Entry{{ID: "ministral-3:3b", ContextLength: 32768}}).WithToolCallRejections(rejections)}
			var drafts []string
			deps := PromptLoopDeps{
				Context: ContextDeps{Tools: reg, Limits: func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() }, BuildMessages: func(_ context.Context, _ *api.Session, h []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
					return h, nil
				}},
				Model:    ModelDeps{LLM: client},
				Closeout: CloseoutDeps{EvaluateContentAnchor: outputPolicyEvaluator(t, newPipeline())},
				Projection: ProjectionDeps{
					AppendDraftVersion: func(_ context.Context, _, _, body, code string) (int, error) {
						if code == "TOOL_CALL_UNPARSED" {
							drafts = append(drafts, body)
						}
						return len(drafts), nil
					},
					AppendMessages: func(context.Context, string, ...api.Message) error { return nil },
					UpdateMessage:  func(context.Context, string, string, api.Message) error { return nil },
				},
			}
			sess := &api.Session{ID: t.Name(), Posture: api.SessionPostureBuild}
			result, err := NewPromptLoopForTest(deps).Run(t.Context(), PromptRunInput{SessionID: sess.ID, Session: sess, History: []api.Message{{Role: api.MessageRoleUser, Content: "Build a page"}}, ProfileID: "explore_readonly", ToolCtx: tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID}}})

			if executed != 0 {
				t.Fatalf("a dropped call executed %d times", executed)
			}
			if tc.wantErr {
				if _, typed := failure.AsProviderToolCallUnparsed(err); !typed || err.Error() != "ollama: "+unparsedSyntaxError {
					t.Fatalf("error = %v, want the provider's original error after the bound", err)
				}
			} else {
				testutil.FailErr(t, "run recovery", err)
				if result.LastAssistantContent != "done" {
					t.Fatalf("content = %q, want the repaired response", result.LastAssistantContent)
				}
			}
			if len(client.requests) != tc.drops+boolInt(!tc.wantErr) {
				t.Fatalf("requests = %d, want %d", len(client.requests), tc.drops+boolInt(!tc.wantErr))
			}
			for i, req := range client.requests {
				feedback := 0
				for _, msg := range req.Messages {
					if len(msg.ToolCalls) > 0 || msg.ToolResult != nil {
						t.Fatalf("request %d carries a tool call or result: %+v", i, msg)
					}
					if strings.Contains(msg.Content, "Code: TOOL_CALL_UNPARSED") {
						feedback++
						if msg.Role == api.MessageRoleAssistant || !strings.Contains(msg.Content, "valid JSON arguments") {
							t.Fatalf("request %d feedback is not host repair copy: %+v", i, msg)
						}
					}
				}
				// A repeated drop refreshes the same host nudge in place.
				if feedback != min(i, 1) {
					t.Fatalf("request %d carries %d feedback messages, want %d", i, feedback, min(i, 1))
				}
			}
			wantDraft := "Writing the page."
			if tc.beforeOutput {
				wantDraft = ""
			}
			if len(drafts) != min(tc.drops, maxToolCallUnparsedRecoveries) {
				t.Fatalf("rejected drafts = %d, want %d", len(drafts), min(tc.drops, maxToolCallUnparsedRecoveries))
			}
			for _, body := range drafts {
				if body != wantDraft {
					t.Fatalf("rejected draft = %q, want the streamed prose %q", body, wantDraft)
				}
			}
		})
	}
}

// outputPolicyPipeline loads the stock policy; each call of the result returns a
// fresh pipeline with content.output enabled.
func outputPolicyPipeline(t *testing.T) func() *oar.GuardPipeline {
	t.Helper()
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	root := testutil.CheckoutRoot(t)
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallFile(filepath.Join(root, "lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml")))
	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	testutil.FailErr(t, "loader", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "policy", err)
	return func() *oar.GuardPipeline {
		pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
		pipeline.EnableAnchor(oar.AnchorContentOutput)
		return pipeline
	}
}

func outputPolicyEvaluator(t *testing.T, pipeline *oar.GuardPipeline) func(context.Context, *api.Session, string, oar.ContentObservation, string, map[string]any) (*guidance.Refusal, bool, string, bool) {
	return func(ctx context.Context, sess *api.Session, anchor string, observation oar.ContentObservation, _ string, _ map[string]any) (*guidance.Refusal, bool, string, bool) {
		if anchor != oar.AnchorContentOutput {
			return nil, false, "", false
		}
		gc := oar.NewGuardContext()
		gc.Session.SessionID = sess.ID
		gc.SetContentObservation(observation)
		result, err := pipeline.EvaluateBlock(ctx, anchor, gc)
		testutil.FailErr(t, "output policy", err)
		if result.Decision == nil {
			return nil, false, "", false
		}
		decision := result.Decision
		body, err := guidance.RenderPolicyCopy(ctx, decision.Code, string(decision.Effect), decision.Copy)
		testutil.FailErr(t, "render output policy", err)
		return guidance.NewRefusal(decision.Code, body).WithPolicyCopy(decision.Copy).WithDetails(decision.Data, nil), true, "", false
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Recovery needs the stock policy decision; a missing or different decision
// leaves the dropped call terminal.
func TestDroppedToolCallNeedsItsPolicyDecision(t *testing.T) {
	cause := &failure.ProviderToolCallUnparsedError{ProviderID: "desktop", Model: "m", Cause: io.ErrUnexpectedEOF}
	req := modelcall.CompletionRequest{Tools: []tools.ToolMeta{{Name: "read"}}}
	other := guidance.NewRefusal("SECRET_IN_OUTPUT", "withheld")
	for _, tc := range []struct {
		decision *guidance.Refusal
		want     error
	}{{nil, cause}, {other, other}} {
		turn := &modelTurn{Closeout: &turnCloseout{Deps: CloseoutDeps{EvaluateContentAnchor: func(context.Context, *api.Session, string, oar.ContentObservation, string, map[string]any) (*guidance.Refusal, bool, string, bool) {
			return tc.decision, tc.decision != nil, "", false
		}}}}
		st := &promptLoopTurnState{}
		got, err := turn.recoverUnparsedToolCall(t.Context(), &api.Session{ID: "s"}, st, req, nil, &modelcall.Completion{Content: "partial"}, cause, false)
		if got != nil || !errors.Is(err, tc.want) || st.output.rejection != nil || st.output.droppedToolCallStreak != 0 {
			t.Fatalf("decision %v: recovered=%v err=%v state=%+v", tc.decision, got, err, st.output)
		}
	}
}
