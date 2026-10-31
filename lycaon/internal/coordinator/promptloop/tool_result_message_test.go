package promptloop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/datamark"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestToolResultMessageRetainsProvenanceAfterMetadata(t *testing.T) {
	for _, origin := range []api.MessageOrigin{api.MessageOriginTool, api.MessageOriginRetrieval} {
		t.Run(string(origin), func(t *testing.T) {
			const body = "retrieved content"
			loop := NewPromptLoop(PromptLoopDeps{})
			run := toolInvocation{
				content:  body,
				facts:    guidance.ToolResultFacts{Outcome: api.ToolResultOutcomeCompleted},
				captures: toolCaptures{completion: &api.ToolCompletion{Operation: "fetch", State: "completed"}},
			}
			call := api.ToolCall{ID: "call-1", Name: "fetch_url", Args: map[string]any{"url": "https://example.com"}}
			message := loop.Tools.composeToolResultMessage(t.Context(), nil, "session-1", call, "assistant-1", origin, &run)
			if datamark.Framed(message.Content) != (origin == api.MessageOriginRetrieval) {
				t.Fatalf("origin %s lost its provenance framing: %q", origin, message.Content)
			}
			if !strings.Contains(message.Content, body) || run.content != body {
				t.Fatalf("message=%q receipt content=%q", message.Content, run.content)
			}
			result := message.ToolResult
			if result == nil || result.Content != message.Content || result.ToolCallID != call.ID || result.AssistantMessageID != "assistant-1" || result.Completion == nil {
				t.Fatalf("result metadata or content lost: %+v", result)
			}
			if message.Authority != api.ContentAuthorityNone || message.TrustTier != api.ContentTrustTierUntrusted {
				t.Fatalf("tool content gained authority: %+v", message)
			}
		})
	}
}

func TestOverlayPromotionSurvivesToolResultRoundTrip(t *testing.T) {
	capture := &tools.ToolInvocationOut{OverlayPromotion: &api.OverlayPromotion{
		Files: []api.FileEditSnapshot{{RootID: "root", Path: "new.go", After: "merged"}},
	}}
	result := &api.ToolResult{Content: "promoted", ToolCallID: "merge-call"}
	applyToolResultSidecars(context.Background(), nil, nil, "project", "session", "session",
		"promote_overlay", result.Content, result, toolCapturesFrom(capture))
	raw, err := json.Marshal(result)
	testutil.FailErr(t, "marshal promotion result", err)
	var restored api.ToolResult
	testutil.FailErr(t, "restore promotion result", json.Unmarshal(raw, &restored))
	if restored.OverlayPromotion == nil || restored.OverlayPromotion.Files[0].After != "merged" {
		t.Fatalf("promotion snapshots missing: %#v", restored.OverlayPromotion)
	}
}

func TestEvidenceCommitFailureStopsEnrichment(t *testing.T) {
	failure := errors.New("evidence store unavailable")
	loop := NewPromptLoop(PromptLoopDeps{
		Tools: ToolsDeps{
			CommitEvidenceToolResult: func(context.Context, string, *api.Session, string, map[string]any, string, string) (string, string, error) {
				return "", "", failure
			},
		},
	})
	message := api.Message{ID: "result", Content: "observed bytes"}
	history := []api.Message{message}
	_, err := loop.Batch.enrichCommittedToolRow(t.Context(), "session", &api.Session{ID: "session"}, history,
		"read", map[string]any{"path": "data.json"}, message.ID, true, &promptLoopTurnState{})
	if !errors.Is(err, failure) {
		t.Fatalf("evidence failure = %v, want store failure", err)
	}
	if history[0].Content != message.Content || len(history[0].EvidenceHandles) != 0 {
		t.Fatal("failed evidence commit changed the stored observation")
	}
}

func TestFailedEffectEvidenceRetainsOutcomeWithoutInventingFileProof(t *testing.T) {
	for _, tc := range []struct {
		tool                  string
		invoked, hostAnswered bool
		status                api.InvocationStatus
		want                  bool
	}{
		{"command", true, false, api.InvocationStatusError, true},
		{"verify", true, false, api.InvocationStatusError, true},
		{"verify", false, false, api.InvocationStatusError, false},
		{"verify", true, true, api.InvocationStatusError, false},
		{"git_commit", true, false, api.InvocationStatusError, true},
		{"git_merge", true, false, api.InvocationStatusError, true},
		{"write", true, false, api.InvocationStatusError, false},
		{"read", true, false, api.InvocationStatusError, false},
		{"command", false, false, api.InvocationStatusError, false},
		{"command", true, true, api.InvocationStatusError, false},
		{"command", true, false, api.InvocationStatusInterrupted, false},
	} {
		contract, ok := toolcontract.Lookup(tc.tool)
		if !ok {
			t.Fatalf("missing contract for %s", tc.tool)
		}
		run := toolInvocation{contract: contract, hostAnswered: tc.hostAnswered,
			facts:   guidance.ToolResultFacts{Outcome: api.ToolResultOutcomeError},
			receipt: &api.InvocationReceipt{Invoked: tc.invoked, Status: tc.status}}
		if got := toolEvidenceEligible(run, tc.tool, nil); got != tc.want {
			t.Fatalf("case %+v: eligible=%v", tc, got)
		}
	}
}

// A yielded process reaches the wire as a typed field.
func TestApplyToolResultSidecars_stampsProcessHandle(t *testing.T) {
	tr := &api.ToolResult{Content: `{"running":true,"handle":"h-1"}`, ToolCallID: "call-1"}
	applyToolResultSidecars(
		context.Background(), nil, nil, "proj-1", "root-1", "root-1",
		"command", `{"running":true,"handle":"h-1"}`, tr,
		toolCaptures{process: &api.ToolProcessHandle{Handle: "h-1", Running: true}},
	)
	if tr.Process == nil {
		t.Fatal("tool result carries no process handle")
	}
	if tr.Process.Handle != "h-1" || !tr.Process.Running {
		t.Fatalf("process = %+v want h-1/running", tr.Process)
	}
}

// Finished calls leave the process field unset.
func TestApplyToolResultSidecars_leavesProcessUnsetWhenNothingRan(t *testing.T) {
	tr := &api.ToolResult{Content: `{"exit_code":0}`, ToolCallID: "call-1"}
	applyToolResultSidecars(
		context.Background(), nil, nil, "proj-1", "root-1", "root-1",
		"command", `{"exit_code":0}`, tr, toolCaptures{},
	)
	if tr.Process != nil {
		t.Fatalf("finished call stamped a process handle: %+v", tr.Process)
	}
}

func TestInvocationOwnerRefPrefersHandlerCapture(t *testing.T) {
	result := &api.ToolResult{Dispatch: &api.WorkerDispatch{WorkerID: "projected-job"}}
	if got := invocationOwnerRef("owner-job", result); got != "owner-job" {
		t.Fatalf("owner ref = %q", got)
	}
}

func TestApplyToolResultSidecarsRetainsResolvedSubject(t *testing.T) {
	out := &tools.ToolInvocationOut{DisplaySubject: "./task den:test:fast"}
	result := &api.ToolResult{Tool: "command_output", ToolArgs: map[string]any{"handle": "exact-handle"}}
	applyToolResultSidecars(t.Context(), nil, nil, "project", "session", "session", "command_output", "done", result, toolCapturesFrom(out))
	if result.DisplaySubject != out.DisplaySubject || result.ToolArgs["handle"] != "exact-handle" {
		t.Fatalf("resolved target or routing identity lost: %+v", result)
	}
}

func TestInvocationIsolationOfBindsPreOwnerAndAttemptedEffectOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		run         toolInvocation
		code        string
		disposition isolation.Disposition
	}{
		{
			name: "pre-owner failure",
			run: toolInvocation{failure: &api.InvocationFailure{
				Code: isolation.CodeWriteRootDenied, Class: api.FailureClassIsolationRejection,
			}},
			code: isolation.CodeWriteRootDenied, disposition: isolation.DispositionHumanDecision,
		},
		{
			name: "attempted effect",
			run:  toolInvocation{facts: guidance.ToolResultFacts{}.WithCode(isolation.CodeTryWriteRoot)},
			code: isolation.CodeTryWriteRoot, disposition: isolation.DispositionRetry,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := invocationIsolationOf(test.run)
			if got == nil || got.Code != test.code || got.Disposition != string(test.disposition) {
				t.Fatalf("isolation outcome = %+v, want %s/%s", got, test.code, test.disposition)
			}
		})
	}
}

// A review raised at the owner's effect seam refuses an operation whose owner
// already ran: the refusal stays a terminal isolation rejection with invoked set.
func TestOwnerSeamTerminalRefusalKeepsInvokedIsolationRejection(t *testing.T) {
	contract, ok := toolcontract.Lookup("write")
	if !ok {
		t.Fatal("missing contract for write")
	}
	loop := NewPromptLoopForTest(PromptLoopDeps{})
	runErr := toolrejection.RenderReject(&toolrejection.ToolReject{
		Code: isolation.CodeControlPlaneDenied, Data: map[string]any{"path": "/state/approvals.yaml", "tool": "write"},
	}, nil)
	run := loop.Tools.finalizeToolRun(t.Context(), &api.Session{ID: "session"}, completedToolRun{
		sessionID: "session", call: api.ToolCall{ID: "call", Name: "write"}, contract: contract,
		toolCtx: tools.ToolContext{
			Effects: tools.InvocationEffects{Out: &tools.ToolInvocationOut{OwnerInvoked: true}},
		},
		runErr: runErr, startedAt: time.Now(),
	})
	if run.reject == nil || !run.invoked || run.failure == nil ||
		run.failure.Code != isolation.CodeControlPlaneDenied ||
		run.failure.Class != api.FailureClassIsolationRejection || run.failure.Retryable {
		t.Fatalf("owner-seam refusal = invoked %v failure %+v", run.invoked, run.failure)
	}
	got := invocationIsolationOf(run)
	if got == nil || got.Code != isolation.CodeControlPlaneDenied ||
		got.Disposition != string(isolation.DispositionControlPlane) {
		t.Fatalf("isolation outcome = %+v", got)
	}
}
