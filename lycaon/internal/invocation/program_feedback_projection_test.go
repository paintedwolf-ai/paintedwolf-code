package invocation_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/curl"
	"github.com/lycaon/lycaon/pkg/api"
)

func feedbackMemory(t *testing.T, state int64) *toolcontract.ProgramMemory {
	t.Helper()
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 32 << 20, State: state})
	testutil.FailErr(t, "program memory", err)
	return memory
}

const feedbackProtected = "fixture-protected-diagnostic"

// diagnosticScreen is the actual bounded capture producer over owner data.
func diagnosticScreen(f programReceiptFixture) invocation.ProgramInputCapture {
	matcher := secretmatch.NewInertMatcher()
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{Name: "token", Secret: feedbackProtected, RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch, NonDisclosable: true}}
	})
	quota := &inputCaptureQuota{}
	return invocation.ProgramInputCapture{Projector: captureprojection.New(matcher, nil), ProgramID: f.program, Scope: captureprojection.Scope{RootSessionID: f.session, SessionID: f.session}, Screening: captureprojection.StreamOptions{WorkingMemory: quota, Bounds: secretmatch.StreamBounds{InputBytes: 1 << 20, OutputBytes: 1 << 20, MemoryBytes: 32 << 20, DetectorWork: 1 << 30}}, MaximumNodes: 1024, MaximumDepth: 64}
}

func TestProgramFailureKeepsActualParentAndEffectsOnProviderProjection(t *testing.T) {
	f := newProgramReceiptFixture(t)
	read := f.child(t)
	f.start(t, read)
	testutil.FailErr(t, "settle actual invoked read", f.recorder.SettleChild(t.Context(), read.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, OwnerInvoked: true, OutputPublishable: true, DetailReference: "output:read-detail"}))
	denied := f.childFor(t, "write")
	code := "COMMAND_EFFECT_NOT_ALLOWED"
	testutil.FailErr(t, "settle refused child", f.recorder.SettleChild(t.Context(), denied.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerRefused, StopCode: code, EffectCertainty: toolcontract.NoEffect}, DetailReference: "output:scope-refusal"}))
	testutil.FailErr(t, "settle program without erasing read", f.recorder.SettleProgram(t.Context(), f.program, toolcontract.ProgramSettlement{State: "rejected", StopCode: code, DetailReference: "output:program-detail"}))
	cause := &toolrejection.ToolReject{Code: code, OwnerRef: denied.Definition().Facts().Contract.Owner, Data: map[string]any{"field": "path", "path": "input", "source_unit_id": "owner-input", "source_start_byte": 2, "source_end_byte": 3, "source_line": 9, "source_column": 4, "large": strings.Repeat("x", 40<<10), "owner_detail": "denied for " + feedbackProtected}}
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: f.session, ToolCallID: "actual-model-call"}}
	memory := feedbackMemory(t, 64<<20)
	prepared, err := toolexecution.PrepareProgramFailure(t.Context(), f.recorder, memory, diagnosticScreen(f), tc, denied, cause)
	testutil.FailErr(t, "prepare actual ledger feedback", err)
	defer prepared.Close()
	if memory.InUse().State == 0 || prepared.Rejection.Code != code || prepared.Rejection.OwnerRef != cause.OwnerRef {
		t.Fatal("preparation changed owner code or lost lease")
	}
	data := prepared.Rejection.Data
	if data["tool"] != "command" || data["native_operation"] != "write" || data["execution_id"] != denied.Invocation().Identity.ExecutionID || data["model_tool_call_id"] != "actual-model-call" || data["program_executed_reads"] != 1 || data["program_replay_available"] != false {
		t.Fatalf("lost child/parent/effect facts: %+v", data)
	}
	if data["operation_source_unit_id"] != denied.Invocation().SourceUnitID || data["operation_source_start_byte"] != denied.Invocation().Span.StartByte || data["source_unit_id"] != "owner-input" || data["source_start_byte"] != 2 || data["source_line"] != 9 {
		t.Fatalf("enclosing stage replaced owner diagnostic coordinates: %+v", data)
	}
	if cause.Data["native_operation"] != nil {
		t.Fatal("supplement mutated owner rejection")
	}
	// Publication carries only the capture-screened diagnostic; the exact owner
	// bytes stay in the host-only reversible inspection value.
	if detail, _ := data["owner_detail"].(string); strings.Contains(detail, feedbackProtected) || !strings.HasPrefix(detail, "denied for ") {
		t.Fatalf("model-visible diagnostic was not screened: %q", detail)
	}
	inspection, err := prepared.Diagnostic()
	testutil.FailErr(t, "retain exact diagnostic inspection", err)
	if inspection.Representation() != "native_diagnostic_inspection_v1" {
		t.Fatalf("diagnostic inspection domain = %q", inspection.Representation())
	}
	exact, releaseExact, err := inspection.Value(t.Context(), memory)
	testutil.FailErr(t, "inspect exact diagnostic", err)
	if exact.(map[string]any)["owner_detail"] != "denied for "+feedbackProtected {
		t.Fatalf("inspection lost exact owner bytes: %+v", exact)
	}
	releaseExact()
	// Exercise the actual OAR boundary with the captured outer arguments. The
	// native path operand must never replace the submitted command object.
	parent := denied.Invocation()
	parent.Identity.ExecutionID, parent.Identity.ParentExecutionID = f.root, ""
	frame, err := toolexecution.FreezeProgramFrame(t.Context(), parent, tc, toolexecution.ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "${op} arg"}}, memory)
	testutil.FailErr(t, "freeze actual model envelope", err)
	defer frame.Close()
	testutil.FailErr(t, "install actual OAR anchors", anchorcatalog.InstallBundled())
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	policy := &oar.Rule{ID: code, Kind: oar.KindPolicy, Anchor: oar.AnchorToolRejected, Effect: oar.EffectBlock, Enforcement: "enforce", When: `tool == "command" && paintedwolf.native_operation == "write" && tool_arg_string("command") == "${op} arg" && tool_arg_string("path") == ""`, Copy: oar.Copy{What: "Actual command envelope selected", Fix: "Inspect the permitted source before preparing the remaining action."}}
	pipeline := oar.NewGuardPipeline(oar.NewRuleSet([]*oar.Rule{policy}), nil, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorToolRejected)
	executor := toolexecution.NewExecutor(nil, nil, "fixture")
	executor.Rejections.SetBlockPlane(&toolfeedback.BlockPlane{Pipeline: pipeline, Renderer: oar.NewRenderer(nil, nil)})
	// A changed worker execution root can be the cause of refusal. Rendering
	// settled authoritative facts does not attempt to authorize that root again.
	tc.Source.WorkerBranchRoot = "changed-worker-tree"
	rendered := executor.Rejections.RenderProgramFailure(t.Context(), frame, tc, prepared)
	refusal, ok := guidance.RefusalFromError(rendered)
	if !ok || refusal.Code() != code || !strings.Contains(refusal.Body, "Actual command envelope selected") || toolrejection.AsToolReject(rendered) == nil {
		t.Fatalf("OAR lost actual model identity or typed owner cause: %v", rendered)
	}
	message := api.Message{ID: "fixture-result", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "command", ToolCallID: "actual-model-call", Outcome: api.ToolResultOutcomeRejected, Feedback: []api.ToolFeedback{{Code: code, Details: data}}}}
	projected := transcript.Project([]api.Message{message})
	var content string
	for _, m := range projected {
		if m.ID == message.ID {
			content = m.Content
		}
	}
	if strings.Contains(content, feedbackProtected) || strings.Contains(refusal.Body, feedbackProtected) {
		t.Fatal("provider projection published protected diagnostic bytes")
	}
	for _, fact := range []string{`"program_executed_reads":1`, `"program_replay_available":false`, `"native_operation":"write"`, `"model_tool_call_id":"actual-model-call"`} {
		if !strings.Contains(content, fact) {
			t.Fatalf("provider omitted actual fact %s: %s", fact, content)
		}
	}
	tc.Identity.ToolCallID = "different-model-call"
	if wrong, err := toolexecution.PrepareProgramFailure(t.Context(), f.recorder, memory, diagnosticScreen(f), tc, denied, cause); wrong != nil || err == nil {
		t.Fatal("unpaired model call received child feedback")
	}
	prepared.Close()
	prepared.Close()
	if _, err := prepared.Diagnostic(); err == nil {
		t.Fatal("closed feedback retained diagnostic inspection")
	}
	frame.Close()
	if err := executor.Rejections.RenderProgramFailure(t.Context(), frame, tc, prepared); err == nil {
		t.Fatal("closed feedback/frame rendered an uncharged observation")
	}
	if memory.InUse().State != 0 {
		t.Fatalf("feedback lease leaked: %d", memory.InUse().State)
	}
}

func TestExternalDiagnosticTextCannotCreateNativeProgramRejection(t *testing.T) {
	memory := feedbackMemory(t, 64<<20)
	var empty toolexecution.PreparedProgramFailure
	empty.Close()
	prepared, err := toolexecution.PrepareProgramFailure(t.Context(), nil, memory, nil, tools.ToolContext{}, toolcontract.BoundOperation{}, errors.New("Code: COMMAND_EFFECT_NOT_ALLOWED; retry the entire program"))
	if err != nil || prepared != nil || memory.Reservations() != 0 {
		t.Fatal("external diagnostic text selected native feedback")
	}
}

// These are actual recorder transitions with stated owner outcomes. Native
// write/HTTP adapters separately qualify the effects that supply those outcomes.
func TestProgramFailureCompactProjectionRetainsCompletedAndUnknownEffects(t *testing.T) {
	f := newProgramReceiptFixture(t)
	write := f.childFor(t, "write")
	f.start(t, write)
	testutil.FailErr(t, "retain completed write outcome", f.recorder.SettleChild(t.Context(), write.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.EffectCompleted, Evidence: toolcontract.OperationEvidence{Owner: write.Definition().Facts().Contract.Owner, Kind: "journal", OwnerRef: "journal:completed-write", RecoveryRef: "journal:completed-write"}}, OwnerInvoked: true, DetailReference: "output:completed-write", AcceptedRevision: "revision2", PublishedRevision: "revision2"}))
	remote := f.childFor(t, "http_request")
	f.start(t, remote)
	// The curl frontend owns the conversion: a request the owner started
	// with no response received settles as an unknown effect, never retried.
	// The host stop here is the program's, recorded beside that outcome.
	_, sent := curl.TransportOutcome(&httpaction.TransportFailure{Reject: &toolrejection.ToolReject{Code: toolrejection.ToolOwnerFailedCode},
		Cause: &outboundhttp.ExchangeError{Method: "POST", RequestsStarted: 1, Err: io.ErrUnexpectedEOF}})
	if sent.EffectCertainty != toolcontract.EffectUnknown {
		t.Fatalf("curl settled a sent request without a response as %+v", sent)
	}
	testutil.FailErr(t, "retain unknown HTTP outcome", f.recorder.SettleChild(t.Context(), remote.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: sent, HostStopCode: toolrejection.ToolOwnerFailedCode, OwnerInvoked: true, DetailReference: "output:remote-unknown"}))
	testutil.FailErr(t, "retain failed aggregate", f.recorder.SettleProgram(t.Context(), f.program, toolcontract.ProgramSettlement{State: "failed", StopCode: toolrejection.ToolOwnerFailedCode, DetailReference: "output:program-partial"}))
	cause := &toolrejection.ToolReject{Code: toolrejection.ToolOwnerFailedCode, OwnerRef: remote.Definition().Facts().Contract.Owner, Data: map[string]any{"large": strings.Repeat("diagnostic", 10<<10)}}
	memory := feedbackMemory(t, 64<<20)
	prepared, err := toolexecution.PrepareProgramFailure(t.Context(), f.recorder, memory, diagnosticScreen(f), tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: f.session, ToolCallID: "actual-model-call"}}, remote, cause)
	testutil.FailErr(t, "prepare partial effect projection", err)
	defer prepared.Close()
	details := prepared.Rejection.Data
	completed, ok := details["program_completed_details"].([]any)
	if !ok || len(completed) != 1 {
		t.Fatalf("completed owner detail absent: %+v", details)
	}
	writeDetail := completed[0].(map[string]any)
	if writeDetail["execution_id"] != write.Invocation().Identity.ExecutionID || writeDetail["recovery_ref"] != "journal:completed-write" || writeDetail["accepted_revision"] != "revision2" || writeDetail["published_revision"] != "revision2" {
		t.Fatalf("write observation lost: %+v", writeDetail)
	}
	unknown, ok := details["program_unknown_details"].([]any)
	if !ok || len(unknown) != 1 || unknown[0].(map[string]any)["execution_id"] != remote.Invocation().Identity.ExecutionID {
		t.Fatalf("unknown dynamic effect lost: %+v", details)
	}
	message := api.Message{ID: "fixture-partial-result", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "command", ToolCallID: "actual-model-call", Outcome: api.ToolResultOutcomeError, Feedback: []api.ToolFeedback{{Code: cause.Code, Details: prepared.Rejection.Data}}}}
	var content string
	for _, m := range transcript.Project([]api.Message{message}) {
		if m.ID == message.ID {
			content = m.Content
		}
	}
	for _, fact := range []string{`"program_completed_effects":1`, `"program_uncertain_effects":1`, `"program_replay_available":false`, `"details_omitted":true`, `"program_detail_ref":"output:program-partial"`, `"program_effect_details_omitted":true`} {
		if !strings.Contains(content, fact) {
			t.Fatalf("compact projection lost %s: %s", fact, content)
		}
	}
	if strings.Contains(content, "program_replay_source_ref") {
		t.Fatal("partial/unknown effects received whole-program replay")
	}
}

type unretainedScreen struct{}

func (unretainedScreen) ProjectOperationDiagnostic(context.Context, toolcontract.ReviewStateAdmission, map[string]any) (map[string]any, func(), error) {
	return map[string]any{}, nil, nil
}

func TestProgramFeedbackFreezesOwnerDetailsBeforeRetainingThem(t *testing.T) {
	f := newProgramReceiptFixture(t)
	read := f.childFor(t, "read")
	const code = "READ_PATH_DENIED"
	testutil.FailErr(t, "settle actual refusal", f.recorder.SettleChild(t.Context(), read.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerRefused, StopCode: code, EffectCertainty: toolcontract.NoEffect}, DetailReference: "output:refusal"}))
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: f.session, ToolCallID: "actual-model-call"}}
	nested := map[string]any{"path": "original", "items": []any{map[string]any{"ranges": []string{"original-range"}}}, "raw": string([]byte{0xff})}
	cause := &toolrejection.ToolReject{Code: code, Data: map[string]any{"details": nested}}
	memory := feedbackMemory(t, 64<<20)
	prepared, err := toolexecution.PrepareProgramFailure(t.Context(), f.recorder, memory, diagnosticScreen(f), tc, read, cause)
	testutil.FailErr(t, "capture actual nested owner diagnostic", err)
	defer prepared.Close()
	nested["path"] = "changed-after-preparation"
	nested["items"].([]any)[0].(map[string]any)["ranges"].([]string)[0] = "changed-range"
	firstRange := func(details map[string]any) any {
		switch ranges := details["items"].([]any)[0].(map[string]any)["ranges"].(type) {
		case []string:
			return ranges[0]
		case []any:
			return ranges[0]
		}
		return nil
	}
	published := prepared.Rejection.Data["details"].(map[string]any)
	if published["path"] != "original" || firstRange(published) != "original-range" {
		t.Fatal("owner mutation changed published diagnostic")
	}
	inspection, err := prepared.Diagnostic()
	testutil.FailErr(t, "retain exact diagnostic", err)
	exact, releaseExact, err := inspection.Value(t.Context(), memory)
	testutil.FailErr(t, "inspect exact diagnostic", err)
	frozen := exact.(map[string]any)["details"].(map[string]any)
	if frozen["path"] != "original" || firstRange(frozen) != "original-range" || frozen["raw"] != string([]byte{0xff}) {
		t.Fatal("owner mutation or lossy JSON changed retained diagnostic inspection")
	}
	releaseExact()
	prepared.Close()
	if memory.InUse().State != 0 || memory.InUse().Working != 0 {
		t.Fatal("diagnostic snapshot leaked state")
	}
	limited := feedbackMemory(t, 1<<20)
	cause.Data["details"] = map[string]any{"oversized": strings.Repeat("x", 2<<20)}
	if refused, err := toolexecution.PrepareProgramFailure(t.Context(), f.recorder, limited, diagnosticScreen(f), tc, read, cause); refused != nil || err == nil || limited.InUse().State != 0 || limited.InUse().Working != 0 {
		t.Fatal("oversized nested diagnostic escaped admission or leaked partial snapshot")
	}
	if refused, err := toolexecution.PrepareProgramFailure(t.Context(), f.recorder, memory, unretainedScreen{}, tc, read, cause); refused != nil || err == nil || memory.InUse().State != 0 {
		t.Fatal("diagnostic screening without a retained projection published feedback")
	}
	cause.Data["details"] = make(chan string)
	if refused, err := toolexecution.PrepareProgramFailure(t.Context(), f.recorder, limited, diagnosticScreen(f), tc, read, cause); refused != nil || err == nil || limited.InUse().State != 0 {
		t.Fatal("unsupported mutable diagnostic escaped snapshot")
	}
}
