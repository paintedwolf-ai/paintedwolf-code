package invocation_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type feedbackMemory struct {
	active, calls     int
	maximum, retained int64
}

func (m *feedbackMemory) ReserveProgramState(_ context.Context, n int64) (func(), error) {
	if n < 0 || (m.maximum > 0 && n > m.maximum-m.retained) {
		return nil, errors.New("fixture aggregate state ceiling")
	}
	m.active++
	m.calls++
	m.retained += n
	return func() { m.active--; m.retained -= n }, nil
}

func TestProgramFailureKeepsActualParentAndEffectsOnProviderProjection(t *testing.T) {
	f := newProgramReceiptFixture(t)
	read := f.child(t)
	f.start(t, read)
	testutil.FailErr(t, "settle actual invoked read", f.recorder.SettleChild(t.Context(), read.Invocation().Identity.ExecutionID, invocation.ChildSettlement{Result: toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, OwnerInvoked: true, OutputPublishable: true, DetailReference: "output:read-detail"}))
	denied := f.childFor(t, "write")
	code := "COMMAND_EFFECT_NOT_ALLOWED"
	testutil.FailErr(t, "settle refused child", f.recorder.SettleChild(t.Context(), denied.Invocation().Identity.ExecutionID, invocation.ChildSettlement{Result: toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerRefused, StopCode: code, EffectCertainty: toolcontract.NoEffect}, DetailReference: "output:scope-refusal"}))
	testutil.FailErr(t, "settle program without erasing read", f.recorder.SettleProgram(t.Context(), f.program, invocation.ProgramSettlement{State: "rejected", StopCode: code, DetailReference: "output:program-detail"}))
	cause := &toolrejection.ToolReject{Code: code, OwnerRef: denied.Definition().Facts().Contract.Owner, Data: map[string]any{"field": "path", "path": "input", "source_unit_id": "owner-input", "source_start_byte": 2, "source_end_byte": 3, "source_line": 9, "source_column": 4, "large": strings.Repeat("x", 40<<10)}}
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: f.session, ToolCallID: "actual-model-call"}}
	memory := &feedbackMemory{}
	prepared, err := toolexecution.PrepareProgramFailure(t.Context(), f.recorder, memory, tc, denied, cause)
	testutil.FailErr(t, "prepare actual ledger feedback", err)
	defer prepared.Close()
	if memory.active < 1 || prepared.Rejection.Code != code || prepared.Rejection.OwnerRef != cause.OwnerRef {
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
	for _, fact := range []string{`"program_executed_reads":1`, `"program_replay_available":false`, `"native_operation":"write"`, `"model_tool_call_id":"actual-model-call"`} {
		if !strings.Contains(content, fact) {
			t.Fatalf("provider omitted actual fact %s: %s", fact, content)
		}
	}
	tc.Identity.ToolCallID = "different-model-call"
	if wrong, err := toolexecution.PrepareProgramFailure(t.Context(), f.recorder, memory, tc, denied, cause); wrong != nil || err == nil {
		t.Fatal("unpaired model call received child feedback")
	}
	prepared.Close()
	prepared.Close()
	frame.Close()
	if err := executor.Rejections.RenderProgramFailure(t.Context(), frame, tc, prepared); err == nil {
		t.Fatal("closed feedback/frame rendered an uncharged observation")
	}
	if memory.active != 0 {
		t.Fatalf("feedback lease leaked: %d", memory.active)
	}
}

func TestExternalDiagnosticTextCannotCreateNativeProgramRejection(t *testing.T) {
	memory := &feedbackMemory{}
	var empty toolexecution.PreparedProgramFailure
	empty.Close()
	prepared, err := toolexecution.PrepareProgramFailure(t.Context(), nil, memory, tools.ToolContext{}, toolcontract.BoundOperation{}, errors.New("Code: COMMAND_EFFECT_NOT_ALLOWED; retry the entire program"))
	if err != nil || prepared != nil || memory.calls != 0 {
		t.Fatal("external diagnostic text selected native feedback")
	}
}

// These are actual recorder transitions with stated owner outcomes. Native
// write/HTTP adapters separately qualify the effects that supply those outcomes.
func TestProgramFailureCompactProjectionRetainsCompletedAndUnknownEffects(t *testing.T) {
	f := newProgramReceiptFixture(t)
	write := f.childFor(t, "write")
	f.start(t, write)
	testutil.FailErr(t, "retain completed write outcome", f.recorder.SettleChild(t.Context(), write.Invocation().Identity.ExecutionID, invocation.ChildSettlement{Result: toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.EffectCompleted, Evidence: toolcontract.OperationEvidence{Owner: write.Definition().Facts().Contract.Owner, Kind: "journal", OwnerRef: "journal:completed-write", RecoveryRef: "journal:completed-write"}}, OwnerInvoked: true, DetailReference: "output:completed-write", AcceptedRevision: "revision2", PublishedRevision: "revision2"}))
	remote := f.childFor(t, "http_request")
	f.start(t, remote)
	testutil.FailErr(t, "retain unknown HTTP outcome", f.recorder.SettleChild(t.Context(), remote.Invocation().Identity.ExecutionID, invocation.ChildSettlement{Result: toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, StopCode: toolrejection.ToolOwnerFailedCode, EffectCertainty: toolcontract.EffectUnknown}, OwnerInvoked: true, DetailReference: "output:remote-unknown"}))
	testutil.FailErr(t, "retain failed aggregate", f.recorder.SettleProgram(t.Context(), f.program, invocation.ProgramSettlement{State: "failed", StopCode: toolrejection.ToolOwnerFailedCode, DetailReference: "output:program-partial"}))
	cause := &toolrejection.ToolReject{Code: toolrejection.ToolOwnerFailedCode, OwnerRef: remote.Definition().Facts().Contract.Owner, Data: map[string]any{"large": strings.Repeat("diagnostic", 10<<10)}}
	memory := &feedbackMemory{}
	prepared, err := toolexecution.PrepareProgramFailure(t.Context(), f.recorder, memory, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: f.session, ToolCallID: "actual-model-call"}}, remote, cause)
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

type feedbackProgress struct{ store *progress.MemoryStore }

func (p feedbackProgress) ProgramProgressState(ctx context.Context, id string) (progress.PrerequisiteState, error) {
	return progress.PrerequisiteState{SessionID: id, RootSessionID: id, Content: p.store.Get(ctx, id)}, nil
}

type feedbackForbiddenStart struct{ called bool }

func (s *feedbackForbiddenStart) BeforeNativeOwner(context.Context, toolcontract.BoundOperation, string, toolcontract.ReviewedOperationInputs) error {
	s.called = true
	return errors.New("policy block dispatched owner start")
}

func TestProgramPolicyBlockSettlesBeforeRenderingEarlierEffects(t *testing.T) {
	f := newProgramReceiptFixture(t)
	write := f.childFor(t, "write")
	f.start(t, write)
	testutil.FailErr(t, "record stated earlier completed effect", f.recorder.SettleChild(t.Context(), write.Invocation().Identity.ExecutionID, invocation.ChildSettlement{Result: toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.EffectCompleted}, OwnerInvoked: true, DetailReference: "output:earlier-effect"}))
	read := f.childFor(t, "read")
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: f.session, ToolCallID: "actual-model-call", Agent: "fixture"}}
	parent := read.Invocation()
	parent.Identity.ExecutionID, parent.Identity.ParentExecutionID = f.root, ""
	memory := &feedbackMemory{}
	frame, err := toolexecution.FreezeProgramFrame(t.Context(), parent, tc, toolexecution.ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "${op} arg"}}, memory)
	testutil.FailErr(t, "capture actual program envelope", err)
	defer frame.Close()
	classes, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	boundary := sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{{ID: "fixture", CommandEffects: classes}})
	executor := toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), tools.NewDefaultRegistry(), "fixture")
	definition := read.Definition()
	surface, err := executor.Metadata.FreezeProgramSurface(t.Context(), platform.ToolFilter{ProfileID: "fixture"}, []toolcontract.OperationDefinition{definition})
	testutil.FailErr(t, "freeze actual profile", err)
	testutil.FailErr(t, "install policy anchors", anchorcatalog.InstallBundled())
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	const code = "FIXTURE_NATIVE_POLICY_STOP"
	rule := &oar.Rule{ID: code, Kind: oar.KindPolicy, Anchor: oar.AnchorToolPreInvoke, Effect: oar.EffectBlock, Enforcement: "enforce", When: `tool == "command" && paintedwolf.native_operation == "read" && tool_arg_string("command") == "${op} arg"`, Copy: oar.Copy{What: "Frozen actual policy occurrence", Fix: "Inspect the existing result before preparing remaining work."}}
	pipeline := oar.NewGuardPipeline(oar.NewRuleSet([]*oar.Rule{rule}), nil, nil)
	pipeline.EnableAnchor(oar.AnchorToolPreInvoke)
	executor.Rejections.SetBlockPlane(&toolfeedback.BlockPlane{Pipeline: pipeline, Renderer: oar.NewRenderer(nil, nil)})
	progressOwner := progress.NewMemoryStore()
	testutil.FailErr(t, "set actual progress", progressOwner.Set(f.session, "- [ ] Inspect"))
	start := &feedbackForbiddenStart{}
	effect := toolexecution.NativeEffect{Operation: read, Stage: toolcontract.ExecutionStage{ExecutionID: read.Invocation().Identity.ExecutionID, OperationID: "read", Argv: []string{"cat"}}, Arguments: map[string]any{}, PrepareReviewed: func(context.Context, map[string]any, tools.ToolContext) (toolexecution.PreparedNativeEffect, error) {
		t.Fatal("blocked operation reached owner preparation")
		return toolexecution.PreparedNativeEffect{}, nil
	}}
	result, cause := executor.InvokeNative(t.Context(), frame, surface, memory, feedbackProgress{progressOwner}, start, effect, tc)
	var policy *toolfeedback.ProgramPolicyBlock
	if !errors.As(cause, &policy) || toolrejection.AsToolReject(cause) == nil || result.OwnerInvoked || start.called {
		t.Fatalf("policy lost typed unrendered stop or invoked owner: %+v %v", result, cause)
	}
	if _, rendered := guidance.RefusalFromError(cause); rendered {
		t.Fatal("policy rendered before recorder settlement")
	}
	testutil.FailErr(t, "settle actual policy-stopped child", f.recorder.SettleChild(t.Context(), read.Invocation().Identity.ExecutionID, invocation.ChildSettlement{Result: result.Producer, DetailReference: "output:policy-stop"}))
	testutil.FailErr(t, "settle stopped program", f.recorder.SettleProgram(t.Context(), f.program, invocation.ProgramSettlement{State: "rejected", StopCode: code, DetailReference: "output:program-stop"}))
	prepared, err := toolexecution.PrepareProgramFailure(t.Context(), f.recorder, memory, tc, read, cause)
	testutil.FailErr(t, "prepare settled policy feedback", err)
	defer prepared.Close()
	// A later rule set cannot rewrite the already occurred policy guidance.
	executor.Rejections.SetBlockPlane(&toolfeedback.BlockPlane{Pipeline: oar.NewGuardPipeline(oar.NewRuleSet(nil), nil, nil), Renderer: oar.NewRenderer(nil, nil)})
	rendered := executor.Rejections.RenderProgramFailure(t.Context(), frame, tc, prepared)
	refusal, ok := guidance.RefusalFromError(rendered)
	if !ok || refusal.Code() != code || !strings.Contains(refusal.Body, "Frozen actual policy occurrence") || refusal.Facts.Feedback[0].Details["program_completed_effects"] != 1 || refusal.Facts.Feedback[0].Details["program_replay_available"] != false {
		t.Fatalf("settled policy lost frozen copy or earlier effects: %v", rendered)
	}
	baseline := memory.calls
	if repeated := executor.Rejections.RenderProgramFailure(t.Context(), frame, tc, prepared); repeated != rendered || memory.calls != baseline {
		t.Fatal("repeated delivery re-evaluated policy or allocated another frozen result")
	}
	prepared.Close()
	frame.Close()
	if memory.active != 0 {
		t.Fatal("settled policy feedback leaked captured frame or SQL leases")
	}
}

func TestProgramFeedbackFreezesOwnerDetailsBeforeRetainingThem(t *testing.T) {
	f := newProgramReceiptFixture(t)
	read := f.childFor(t, "read")
	const code = "READ_PATH_DENIED"
	testutil.FailErr(t, "settle actual refusal", f.recorder.SettleChild(t.Context(), read.Invocation().Identity.ExecutionID, invocation.ChildSettlement{Result: toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerRefused, StopCode: code, EffectCertainty: toolcontract.NoEffect}, DetailReference: "output:refusal"}))
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: f.session, ToolCallID: "actual-model-call"}}
	nested := map[string]any{"path": "original", "items": []any{map[string]any{"ranges": []string{"original-range"}}}, "raw": string([]byte{0xff})}
	cause := &toolrejection.ToolReject{Code: code, Data: map[string]any{"details": nested}}
	memory := &feedbackMemory{}
	prepared, err := toolexecution.PrepareProgramFailure(t.Context(), f.recorder, memory, tc, read, cause)
	testutil.FailErr(t, "capture actual nested owner diagnostic", err)
	defer prepared.Close()
	nested["path"] = "changed-after-preparation"
	nested["items"].([]any)[0].(map[string]any)["ranges"].([]string)[0] = "changed-range"
	frozen := prepared.Rejection.Data["details"].(map[string]any)
	if frozen["path"] != "original" || frozen["items"].([]any)[0].(map[string]any)["ranges"].([]string)[0] != "original-range" || frozen["raw"] != string([]byte{0xff}) {
		t.Fatal("owner mutation or lossy JSON changed retained diagnostic")
	}
	prepared.Close()
	if memory.retained != 0 || memory.active != 0 {
		t.Fatal("diagnostic snapshot leaked state")
	}
	limited := &feedbackMemory{maximum: 1 << 20}
	cause.Data["details"] = map[string]any{"oversized": strings.Repeat("x", 2<<20)}
	if refused, err := toolexecution.PrepareProgramFailure(t.Context(), f.recorder, limited, tc, read, cause); refused != nil || err == nil || limited.retained != 0 || limited.active != 0 {
		t.Fatal("oversized nested diagnostic escaped admission or leaked partial snapshot")
	}
	cause.Data["details"] = make(chan string)
	if refused, err := toolexecution.PrepareProgramFailure(t.Context(), f.recorder, limited, tc, read, cause); refused != nil || err == nil || limited.retained != 0 {
		t.Fatal("unsupported mutable diagnostic escaped snapshot")
	}
}
