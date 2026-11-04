package promptloop_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"

	"github.com/lycaon/lycaon/pkg/api"
)

type markdownRunLedger struct{ ledger evidence.Ledger }

func (r markdownRunLedger) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return r.ledger, nil
}

func (markdownRunLedger) WorkerLegs(context.Context, string, time.Time) ([]guidance.EvidenceLeg, error) {
	return nil, nil
}

type markdownRun struct {
	store    *store.Memory
	session  *api.Session
	deps     promptloop.PromptLoopDeps
	outputs  []string
	versions []string
	retained guidance.RetainedCloseout
}

// newMarkdownRun drives the loop with a redacting store and a fixed ledger; the
// first model response follows whatever history the caller gives Run.
func newMarkdownRun(t *testing.T, completions ...*modelcall.Completion) *markdownRun {
	t.Helper()
	r := &markdownRun{store: store.NewMemory()}
	session, err := r.store.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "project")
	testutil.FailErr(t, "create session", err)
	r.session = session
	deps := promptloop.StoreDeps(r.store)
	deps.Model.LLM = &sequentialLLMClient{completions: completions}
	deps.Context.Tools = tools.NewStubRegistry()
	deps.Context.Policy = &recordingToolPolicy{}
	deps.Context.PromptTurnSurface = func(string) string { return "implement_synthesis" }
	deps.Context.CoordinatorFrame = staticCoordinatorContext{run: api.CoordinatorRunContext{RunID: "run-1", WorkflowID: "reporttest", CurrentPhase: "report"}}
	deps.Projection.RedactMessageForStorage = func(_ context.Context, msg api.Message) (api.Message, bool) {
		if !strings.Contains(msg.Content, "s3cret") {
			return msg, false
		}
		msg.Content = strings.ReplaceAll(msg.Content, "s3cret", "[REDACTED VALUE]")
		return msg, true
	}
	deps.Projection.SettleModelOutput = func(ctx context.Context, out store.ModelOutput) (store.ModelOutput, error) {
		r.outputs = append(r.outputs, out.Content)
		return r.store.SettleModelOutput(ctx, out)
	}
	deps.Projection.AppendDraftVersion = func(ctx context.Context, sessionID, messageID, body, code string) (int, error) {
		r.versions = append(r.versions, body)
		return r.store.AppendDraftVersion(ctx, sessionID, messageID, body, code)
	}
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hints", err)
	deps.Closeout.HintConfig = hints
	deps.Closeout.RejectFmt = guidance.NewStaticRejectFormatter(hints)
	deps.Closeout.EvidenceLedger = markdownRunLedger{ledger: evidence.AssembleLedger([]evidence.Record{{
		Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "a.go",
		Body: []string{"1|one", "2|two", "3|three"}, LineRanges: []evidence.LineRange{{Start: 1, End: 3}},
	}})}
	deps.Closeout.CitationRoots = func(context.Context, *api.Session) evidence.CitationRoots { return evidence.CitationRoots{} }
	deps.Closeout.EvaluateCloseoutBlock = func(_ context.Context, _ *api.Session, gc *oar.GuardContext) (*oar.Decision, error) {
		if gc.Rejection.RejectObservation == guidance.PresentationObservation(guidance.PresentImageUnobservedCode) {
			return &oar.Decision{Code: guidance.PresentImageUnobservedCode, Effect: oar.EffectBlock, Data: gc.RejectData[guidance.PresentImageUnobservedCode]}, nil
		}
		if len(gc.Grounding.UnobservedCitedHandles) == 0 {
			return nil, nil
		}
		return &oar.Decision{Code: guidance.SynthHandleNotInLegsCode, Effect: oar.EffectBlock, Data: gc.RejectData[guidance.SynthHandleNotInLegsCode]}, nil
	}
	deps.Closeout.CloseoutStallState = func(context.Context, string) guidance.RetainedCloseout { return r.retained }
	deps.Closeout.NoteCloseoutGroundingReject = func(_ context.Context, _, code, key string, draft guidance.CloseoutDraft, unread []jsonshape.Issue) (int, string) {
		r.retained.Active = true
		r.retained.Attempt++
		r.retained.PrevKey = key
		r.retained.ForcedBy = append(r.retained.ForcedBy, code)
		r.retained.Unread = unread
		r.retained.Retain(draft)
		r.retained.BodyRepair = guidance.PresentationObservation(code) != ""
		return r.retained.Attempt, ""
	}
	deps.Closeout.ClearCloseoutStall = func(context.Context, string) { r.retained = guidance.RetainedCloseout{} }
	r.deps = deps
	return r
}

func (r *markdownRun) run(t *testing.T, history []api.Message) (*promptloop.PromptRunResult, api.Message) {
	t.Helper()
	ctx := t.Context()
	execution, err := r.store.BeginTurn(ctx, store.TurnStart{SessionID: r.session.ID, ProjectID: r.session.ProjectID, Origin: store.TurnOriginUser, InputJSON: "{}"})
	testutil.FailErr(t, "begin turn", err)
	result, err := promptloop.NewPromptLoopForTest(r.deps).Run(ctx, promptloop.PromptRunInput{
		SessionID: r.session.ID, Session: r.session, TurnID: execution.Turn.ID, AttemptID: execution.Attempt.ID,
		History: history, ProfileID: "coordinator",
		ToolCtx: tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: r.session.ID}},
	})
	testutil.FailErr(t, "run the turn", err)
	stored, err := r.store.GetMessages(ctx, r.session.ID)
	testutil.FailErr(t, "read messages", err)
	for _, message := range stored {
		if message.ID == result.LastAssistantID {
			return result, message
		}
	}
	t.Fatalf("no committed row %q in %+v", result.LastAssistantID, stored)
	return nil, api.Message{}
}

func reportTopology(t *testing.T, message api.Message) bool {
	t.Helper()
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "reporttest", Version: "1.0.0",
		Controls: workflowdef.ManifestControls{PhaseAdvance: workflowdef.PhaseAdvanceHost},
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "report", CompleteWhen: workflowdef.CompleteWhenGatesSatisfied, Gates: []string{"topology_report_delivered"}, Next: "done"},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
	return workflow.ReportPhaseCompletion(message, &api.WorkflowRun{ID: "run-1", CurrentPhase: "report", Status: api.WorkflowRunStatusRunning}, manifest)
}

func citedHistory() []api.Message {
	return []api.Message{
		{ID: "user", Role: api.MessageRoleUser, Content: "Inspect it."},
		{ID: "call", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "r1", Name: "read"}}},
		{ID: "result", Role: api.MessageRoleTool, EvidenceHandles: []string{"read#1"}, ToolResult: &api.ToolResult{ToolCallID: "r1", Tool: "read", Outcome: api.ToolResultOutcomeCompleted}},
	}
}

// D2, D5: over Markdown the real run commits a grounded completion report that
// satisfies topology_report_delivered.
func TestMarkdownRunCommitsDeliverableReport(t *testing.T) {
	r := newMarkdownRun(t, &modelcall.Completion{Content: "The cause is here [read#1:2].\n[verify: targeted - Ran it]"})
	_, message := r.run(t, citedHistory())
	if message.Content != "The cause is here [a.go:2](a.go#L2)." {
		t.Fatalf("display = %q", message.Content)
	}
	if message.Grounding == nil || !message.Grounding.Traced || message.Grounding.Verification == nil {
		t.Fatalf("grounding = %+v", message.Grounding)
	}
	if !reportTopology(t, message) {
		t.Fatalf("topology_report_delivered unmet by %+v", message)
	}
}

// AC-CLO-02, D5: a tool-free reply is delivered too, honestly evidence-optional.
func TestMarkdownRunDeliversEvidenceOptionalReply(t *testing.T) {
	r := newMarkdownRun(t, &modelcall.Completion{Content: "You're welcome."})
	_, message := r.run(t, []api.Message{{ID: "user", Role: api.MessageRoleUser, Content: "Thanks."}})
	g := message.Grounding
	if g == nil || !g.EvidenceOptional || !g.Traced || g.HostAssembled {
		t.Fatalf("grounding = %+v", g)
	}
	if message.Kind != api.MessageKindCompletionReport || !reportTopology(t, message) {
		t.Fatalf("message = %+v", message)
	}
}

// Decision 003: training reads model_outputs for the screened text exactly as the
// model wrote it, citations and directives included; messages hold what the user
// reads. Decision 004: the parse runs on that screened revision, so a redaction that
// shifts bytes cannot shift a citation.
func TestModelOutputsKeepScreenedModelTextAndMessagesHoldPresentation(t *testing.T) {
	const raw = "The key s3cret lives here [read#1:2].\n[verify: inspection - Read it]\n\n```\n[read#1:3]\n```"
	const screened = "The key [REDACTED VALUE] lives here [read#1:2].\n[verify: inspection - Read it]\n\n```\n[read#1:3]\n```"
	r := newMarkdownRun(t, &modelcall.Completion{Content: raw})
	_, message := r.run(t, citedHistory())
	if len(r.outputs) != 1 || r.outputs[0] != screened {
		t.Fatalf("model_outputs.content = %q, want the screened model text", r.outputs)
	}
	const presented = "The key [REDACTED VALUE] lives here [a.go:2](a.go#L2).\n\n```\n[read#1:3]\n```"
	if message.Content != presented {
		t.Fatalf("messages.content = %q, want %q", message.Content, presented)
	}
}

// AC-CLO-06, AC-CLO-11: a repair runs through the real loop; the rejected raw draft
// stays for inspection, training keeps both responses, and the narrative is the
// first response's.
func TestMarkdownRunRepairsByOccurrence(t *testing.T) {
	r := newMarkdownRun(t,
		&modelcall.Completion{Content: "The cause is here [read#9:2]."},
		&modelcall.Completion{Content: "[cite: c1 => read#1:2]"},
	)
	_, message := r.run(t, citedHistory())
	if message.Content != "The cause is here [a.go:2](a.go#L2)." {
		t.Fatalf("display = %q", message.Content)
	}
	if message.Grounding == nil || !message.Grounding.Traced || message.Grounding.HostAssembled {
		t.Fatalf("grounding = %+v", message.Grounding)
	}
	if len(r.outputs) != 2 || r.outputs[0] != "The cause is here [read#9:2]." || r.outputs[1] != "[cite: c1 => read#1:2]" {
		t.Fatalf("model outputs = %q", r.outputs)
	}
	if len(r.versions) != 1 || r.versions[0] != "The cause is here [read#9:2]." {
		t.Fatalf("rejected draft versions = %q", r.versions)
	}
	if !reportTopology(t, message) {
		t.Fatalf("topology_report_delivered unmet by %+v", message)
	}
}

// A JSON-only response states nothing the host can accept: no report is delivered
// and no grounding claims otherwise.
func TestMarkdownRunNeverAcceptsBareJSON(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"report envelope", `{"synthesis":"All done.","cited_evidence":[{"evidence":"read#1"}]}`},
		{"retired tool call", `{"name":"surface_note","arguments":{"summary":"All done."}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newMarkdownRun(t, &modelcall.Completion{Content: tc.content})
			_, message := r.run(t, citedHistory())
			if message.Kind == api.MessageKindCompletionReport || message.Grounding != nil || reportTopology(t, message) {
				t.Fatalf("bare JSON accepted: %+v", message)
			}
		})
	}
}

type recordedReplay struct {
	promptloopStreamStub
	content string
	tokens  []string
}

func (r *recordedReplay) CacheReplay(_, content string, tokens []string) {
	r.content, r.tokens = content, tokens
}

type promptloopStreamStub struct{}

func (promptloopStreamStub) CacheLive(string, string, string, []string, int) {}
func (promptloopStreamStub) Project(context.Context, string, api.Message) error {
	return nil
}
func (promptloopStreamStub) Finish(context.Context, string) {}

// Replay reads the row, which holds what the user was shown. A coordinator's raw
// nodes never sit in the replay cache for a reconnect to pick up.
func TestReplayCacheHoldsNoRawCitationNodes(t *testing.T) {
	r := newMarkdownRun(t, &modelcall.Completion{Content: "The cause is here [read#1:2]."})
	replay := &recordedReplay{}
	r.deps.Projection.Streams = replay
	_, message := r.run(t, citedHistory())
	if replay.content != "" || len(replay.tokens) != 0 {
		t.Fatalf("replay cache = %q %v", replay.content, replay.tokens)
	}
	if strings.Contains(message.Content, "[read#1") {
		t.Fatalf("accepted row = %q", message.Content)
	}
}

// An unobserved image is refused and its retry replaces the body. The delivered
// answer is the retry's, with nothing left of the fake capture.
func TestMarkdownRunRepairsAnUnobservedImageByReplacingTheBody(t *testing.T) {
	r := newMarkdownRun(t,
		&modelcall.Completion{Content: "The cause is here [read#1:2].\n\n![capture](https://placeholder.example/648x440?text=Capture)"},
		&modelcall.Completion{Content: "The cause is on the second line [read#1:2]."},
	)
	_, message := r.run(t, citedHistory())
	if message.Content != "The cause is on the second line [a.go:2](a.go#L2)." {
		t.Fatalf("display = %q", message.Content)
	}
	if message.Grounding == nil || !message.Grounding.Traced || message.Grounding.HostAssembled {
		t.Fatalf("grounding = %+v", message.Grounding)
	}
}
