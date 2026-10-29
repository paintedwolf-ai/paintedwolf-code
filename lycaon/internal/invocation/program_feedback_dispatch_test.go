package invocation_test

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
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
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
)

type dispatchPolicy struct{ effects toolcontract.CommandEffects }

func (*dispatchPolicy) Evaluate(context.Context, platform.PolicyContext) (*platform.PolicyDecision, error) {
	return &platform.PolicyDecision{Allowed: true}, nil
}
func (*dispatchPolicy) CommandOperationAllowed(context.Context, string, string, string) (bool, error) {
	return true, nil
}
func (p *dispatchPolicy) FreezeCommandProfile(ctx context.Context, _ string) (context.Context, toolcontract.CommandEffects, error) {
	return ctx, p.effects, nil
}

type dispatchProgress struct{ content string }

func (p dispatchProgress) ProgramProgressState(_ context.Context, id string) (progress.PrerequisiteState, error) {
	return progress.PrerequisiteState{SessionID: id, RootSessionID: id, Content: p.content}, nil
}

type validatedEnvelope struct{ catalog string }

func (v validatedEnvelope) ValidatedProgramIdentity(context.Context) (string, string, error) {
	return strings.Repeat("a", 64), v.catalog, nil
}

type feedbackForbiddenStart struct{ called bool }

func (s *feedbackForbiddenStart) BeforeNativeOwner(context.Context, toolcontract.BoundOperation, string, toolcontract.ReviewedOperationInputs) error {
	s.called = true
	return errors.New("policy block dispatched owner start")
}

func stockFeedbackBlockPlane(t *testing.T) *toolfeedback.BlockPlane {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	moduleRoot := filepath.Join(filepath.Dir(file), "..", "..")
	repoRoot := filepath.Join(moduleRoot, "..")
	testutil.FailErr(t, "install catalog", anchorcatalog.InstallFile(filepath.Join(moduleRoot, "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")))
	loader, err := oar.NewLoader(filepath.Join(repoRoot, "schemas"))
	testutil.FailErr(t, "oar loader", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "load stock policy", err)
	pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorToolRejected)
	pipeline.EnableAnchor(oar.AnchorToolPreInvoke)
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load stock hints", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	return &toolfeedback.BlockPlane{Pipeline: pipeline, Renderer: oar.NewRenderer(guidance.NewStaticRejectFormatter(cfg), nil)}
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
	start := &feedbackForbiddenStart{}
	effect := toolexecution.NativeEffect{Operation: read, Stage: toolcontract.ExecutionStage{ExecutionID: read.Invocation().Identity.ExecutionID, OperationID: "read", Argv: []string{"cat"}}, Arguments: map[string]any{}, PrepareReviewed: func(context.Context, map[string]any, tools.ToolContext) (toolexecution.PreparedNativeEffect, error) {
		t.Fatal("blocked operation reached owner preparation")
		return toolexecution.PreparedNativeEffect{}, nil
	}}
	result, cause := executor.InvokeNative(t.Context(), frame, surface, memory, dispatchProgress{content: "- [ ] Inspect"}, start, effect, tc)
	var policy *toolfeedback.ProgramPolicyBlock
	if !errors.As(cause, &policy) || toolrejection.AsToolReject(cause) == nil || result.OwnerInvoked || start.called {
		t.Fatalf("policy lost typed unrendered stop or invoked owner: %+v %v", result, cause)
	}
	if _, rendered := guidance.RefusalFromError(cause); rendered {
		t.Fatal("policy rendered before recorder settlement")
	}
	testutil.FailErr(t, "settle actual policy-stopped child", f.recorder.SettleChild(t.Context(), read.Invocation().Identity.ExecutionID, invocation.ChildSettlement{Result: result.Producer, DetailReference: "output:policy-stop"}))
	testutil.FailErr(t, "settle stopped program", f.recorder.SettleProgram(t.Context(), f.program, invocation.ProgramSettlement{State: "rejected", StopCode: code, DetailReference: "output:program-stop"}))
	prepared, err := toolexecution.PrepareProgramFailure(t.Context(), f.recorder, memory, diagnosticScreen(f), tc, read, cause)
	testutil.FailErr(t, "prepare settled policy feedback", err)
	defer prepared.Close()
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

func TestProgramProgressEnvelopeRefusalFeedbackThroughRealEntry(t *testing.T) {
	f := newProgramReceiptFixture(t)
	schemaDir := filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas")
	schemas, err := toolschema.LoadSchemaDir(schemaDir)
	testutil.FailErr(t, "load schemas", err)
	registry, err := tools.NewCatalogRegistry(schemas)
	testutil.FailErr(t, "catalog registry", err)
	testutil.FailErr(t, "register update_progress", registry.Register("update_progress", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "", errors.New("unexpected json handler call")
	}))
	executor := toolexecution.NewExecutor(nil, registry, "fixture")
	executor.Rejections.SetBlockPlane(stockFeedbackBlockPlane(t))
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{Agent: "fixture", SessionID: f.session, ToolCallID: "actual-model-call"}}
	parent := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: f.program, NodeID: "cat-node", ExecutionID: f.root}, ActorID: "fixture-person", SessionID: f.session, Scope: toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope}, SourceUnitID: "submitted", Span: f.span, CatalogDigest: toolcontract.LanguageCatalogDigest()}
	memory := &feedbackMemory{}
	frame, err := toolexecution.FreezeProgramFrame(t.Context(), parent, tc, toolexecution.ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "${op} arg"}}, memory)
	testutil.FailErr(t, "freeze frame", err)
	defer frame.Close()
	proof := validatedEnvelope{catalog: parent.CatalogDigest}
	update := &toolexecution.ProgramProgressUpdate{ExecutionID: "progress-exec-1", Arguments: map[string]any{"content": "- [ ] outer\n  - [ ] nested"}}
	_, envErr := executor.PrepareProgramEnvelope(t.Context(), frame, proof, update, memory, tc)
	reject := toolrejection.AsToolReject(envErr)
	if reject == nil {
		t.Fatalf("expected typed progress owner reject, got %v", envErr)
	}
	testutil.FailErr(t, "record progress refusal", f.recorder.RecordProgressRefusal(t.Context(), f.program, "output:progress-refused"))
	testutil.FailErr(t, "settle program on progress refusal", f.recorder.SettleProgram(t.Context(), f.program, invocation.ProgramSettlement{State: "rejected", StopCode: reject.Code, DetailReference: "output:program-progress-refused"}))
	prepared, err := toolexecution.PrepareProgramProgressFailure(t.Context(), f.recorder, memory, diagnosticScreen(f), frame, tc, envErr)
	testutil.FailErr(t, "prepare progress failure feedback", err)
	defer prepared.Close()
	rendered := executor.Rejections.RenderProgramFailure(t.Context(), frame, tc, prepared)
	if _, ok := guidance.RefusalFromError(rendered); !ok || prepared.Rejection.Data["program_progress_state"] != "rejected" || prepared.Rejection.Data["program_replay_available"] != true {
		t.Fatalf("progress failure feedback lost state or replay: %v %+v", rendered, prepared.Rejection.Data)
	}
}

func TestProgramNativeDispatchRealEntryFixtures(t *testing.T) {
	readOnlyEffects, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	testutil.FailErr(t, "readonly command effects", err)
	plane := stockFeedbackBlockPlane(t)

	runNative := func(t *testing.T, f programReceiptFixture, profile string, effects toolcontract.CommandEffects, bound toolcontract.BoundOperation, argv []string, offered []string, ownerFn func(context.Context, tools.ToolContext) (toolcontract.ProducerResult, error)) (toolexecution.NativeInvocationResult, error, *toolexecution.Executor, toolexecution.ProgramFrame, tools.ToolContext, *feedbackMemory) {
		t.Helper()
		executor := toolexecution.NewExecutor(&dispatchPolicy{effects: effects}, tools.NewDefaultRegistry(), profile)
		executor.Rejections.SetBlockPlane(plane)
		surface, err := executor.Metadata.FreezeProgramSurface(t.Context(), platform.ToolFilter{ProfileID: profile}, []toolcontract.OperationDefinition{bound.Definition()})
		testutil.FailErr(t, "freeze surface", err)
		tc := tools.ToolContext{Identity: tools.InvocationIdentity{Agent: profile, SessionID: f.session, ToolCallID: "actual-model-call"}, Turn: tools.InvocationTurn{TurnOfferedToolNames: offered}}
		parent := bound.Invocation()
		parent.Identity.ExecutionID, parent.Identity.ParentExecutionID = f.root, ""
		memory := &feedbackMemory{}
		frame, err := toolexecution.FreezeProgramFrame(t.Context(), parent, tc, toolexecution.ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "${op} arg"}}, memory)
		testutil.FailErr(t, "freeze frame", err)
		t.Cleanup(frame.Close)
		effect := toolexecution.NativeEffect{
			Operation: bound,
			Stage:     toolcontract.ExecutionStage{ExecutionID: bound.Invocation().Identity.ExecutionID, OperationID: bound.Definition().Facts().Identity, Argv: argv},
			Arguments: map[string]any{"path": "input"},
			PrepareReviewed: func(context.Context, map[string]any, tools.ToolContext) (toolexecution.PreparedNativeEffect, error) {
				return toolexecution.PreparedNativeEffect{Operation: bound, InvokeOwner: ownerFn}, nil
			},
		}
		observer := invocation.ProgramAdmissionRecorder{Recorder: f.recorder, Inputs: &retainedInputsFixture{}}
		res, invokeErr := executor.InvokeNative(t.Context(), frame, surface, memory, dispatchProgress{content: "- [ ] Active step"}, observer, effect, tc)
		return res, invokeErr, executor, frame, tc, memory
	}

	assertRenderedFeedback := func(t *testing.T, f programReceiptFixture, bound toolcontract.BoundOperation, res toolexecution.NativeInvocationResult, invokeErr error, executor *toolexecution.Executor, frame toolexecution.ProgramFrame, tc tools.ToolContext, memory *feedbackMemory, state, wantCode string) (*guidance.Refusal, string) {
		t.Helper()
		testutil.FailErr(t, "settle child", f.recorder.SettleChild(t.Context(), bound.Invocation().Identity.ExecutionID, invocation.ChildSettlement{Result: res.Producer, OwnerInvoked: res.OwnerInvoked, DetailReference: "output:child"}))
		testutil.FailErr(t, "settle program", f.recorder.SettleProgram(t.Context(), f.program, invocation.ProgramSettlement{State: state, StopCode: wantCode, DetailReference: "output:program"}))
		prepared, err := toolexecution.PrepareProgramFailure(t.Context(), f.recorder, memory, diagnosticScreen(f), tc, bound, invokeErr)
		testutil.FailErr(t, "prepare program failure", err)
		t.Cleanup(prepared.Close)
		rendered := executor.Rejections.RenderProgramFailure(t.Context(), frame, tc, prepared)
		refusal, ok := guidance.RefusalFromError(rendered)
		if !ok || refusal.Code() != wantCode {
			t.Fatalf("expected rendered refusal %s, got %v (err=%v)", wantCode, refusal, rendered)
		}
		msg := api.Message{ID: "m1", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "command", ToolCallID: "actual-model-call", Outcome: api.ToolResultOutcomeRejected, Feedback: []api.ToolFeedback{{Code: wantCode, Details: prepared.Rejection.Data}}}}
		for _, m := range transcript.Project([]api.Message{msg}) {
			if m.ID == msg.ID {
				return refusal, m.Content
			}
		}
		return refusal, ""
	}

	// 1. Read-only denial through real InvokeNative entry.
	f1 := newProgramReceiptFixture(t)
	w1 := f1.childFor(t, "write")
	res1, err1, ex1, fr1, tc1, mem1 := runNative(t, f1, "worker_readonly", readOnlyEffects, w1, []string{"tee", "input"}, nil, func(context.Context, tools.ToolContext) (toolcontract.ProducerResult, error) {
		t.Fatal("owner invoked on readonly surface")
		return toolcontract.ProducerResult{}, nil
	})
	if res1.OwnerInvoked {
		t.Fatal("readonly denial invoked owner")
	}
	_, proj1 := assertRenderedFeedback(t, f1, w1, res1, err1, ex1, fr1, tc1, mem1, "rejected", "COMMAND_EFFECT_NOT_ALLOWED")
	if !strings.Contains(proj1, `"program_replay_available":true`) {
		t.Fatalf("readonly refusal lost replayability: %s", proj1)
	}

	cases := []struct {
		op, cmd, code string
		offered       []string
		reject        *toolrejection.ToolReject
	}{
		{op: "move", cmd: "mv", code: "MOVE_PATH_DENIED", reject: &toolrejection.ToolReject{Code: "MOVE_PATH_DENIED", Data: map[string]any{"path": "../outside", "reason": "outside project root", "scope": "project root"}}},
		{op: "write", cmd: "tee", code: "TEXT_WRITE_CONFLICT", reject: &toolrejection.ToolReject{Code: "TEXT_WRITE_CONFLICT", Data: map[string]any{"path": "input", "text_base_changed": true}}},
		{op: "write", cmd: "tee", code: "USE_WRITE_NATIVE", offered: []string{"command", "request_tools"}, reject: &toolrejection.ToolReject{Code: "USE_WRITE_NATIVE", Data: map[string]any{"tool": "command", "command": "tee input", "path": "input", "tool_loadable": true, "native_replacement_tools": []string{"write"}}}},
	}
	for _, tcCase := range cases {
		fc := newProgramReceiptFixture(t)
		child := fc.childFor(t, tcCase.op)
		res, invokeErr, ex, fr, tc, mem := runNative(t, fc, "fixture", child.Effects(), child, []string{tcCase.cmd, "input"}, tcCase.offered, func(context.Context, tools.ToolContext) (toolcontract.ProducerResult, error) {
			return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerRefused, StopCode: tcCase.code, EffectCertainty: toolcontract.NoEffect}, tcCase.reject
		})
		refusal, proj := assertRenderedFeedback(t, fc, child, res, invokeErr, ex, fr, tc, mem, "rejected", tcCase.code)
		if tcCase.code == "USE_WRITE_NATIVE" && (!strings.Contains(refusal.Body, "request_tools") || !strings.Contains(proj, `"tool_loadable":true`)) {
			t.Fatalf("deferred domain tool lost request_tools recovery: body=%q proj=%s", refusal.Body, proj)
		}
	}

	// 5. Partial write + unknown HTTP through real InvokeNative entry, and 6. Misleading external stderr.
	f5 := newProgramReceiptFixture(t)
	w5 := f5.childFor(t, "write")
	resW, errW, _, _, _, _ := runNative(t, f5, "fixture", w5.Effects(), w5, []string{"tee", "input"}, nil, func(context.Context, tools.ToolContext) (toolcontract.ProducerResult, error) {
		return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.EffectCompleted, Evidence: toolcontract.OperationEvidence{Owner: "filesystem", Kind: "journal", OwnerRef: "journal:w5", RecoveryRef: "journal:w5"}}, nil
	})
	testutil.FailErr(t, "completed write", errW)
	testutil.FailErr(t, "settle completed write", f5.recorder.SettleChild(t.Context(), w5.Invocation().Identity.ExecutionID, invocation.ChildSettlement{Result: resW.Producer, OwnerInvoked: resW.OwnerInvoked, DetailReference: "output:w5", AcceptedRevision: "rev2", PublishedRevision: "rev2"}))
	h5 := f5.childFor(t, "http_request")
	resH, errH, exH, frH, tcH, memH := runNative(t, f5, "fixture", h5.Effects(), h5, []string{"curl", "input"}, nil, func(context.Context, tools.ToolContext) (toolcontract.ProducerResult, error) {
		return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, StopCode: toolrejection.ToolOwnerFailedCode, EffectCertainty: toolcontract.EffectUnknown}, &toolrejection.ToolReject{Code: toolrejection.ToolOwnerFailedCode, Data: map[string]any{"reason": "transport dropped"}}
	})
	_, proj5 := assertRenderedFeedback(t, f5, h5, resH, errH, exH, frH, tcH, memH, "failed", toolrejection.ToolOwnerFailedCode)
	for _, want := range []string{`"program_completed_effects":1`, `"program_uncertain_effects":1`, `"program_replay_available":false`} {
		if !strings.Contains(proj5, want) {
			t.Fatalf("partial write + unknown HTTP projection missing %s: %s", want, proj5)
		}
	}
	f6 := newProgramReceiptFixture(t)
	g6 := f6.childFor(t, "grep")
	_, err6, _, _, tc6, mem6 := runNative(t, f6, "fixture", g6.Effects(), g6, []string{"grep", "input"}, nil, func(context.Context, tools.ToolContext) (toolcontract.ProducerResult, error) {
		return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, errors.New("stderr: Code: COMMAND_EFFECT_NOT_ALLOWED")
	})
	if prep6, prepErr := toolexecution.PrepareProgramFailure(t.Context(), f6.recorder, mem6, diagnosticScreen(f6), tc6, g6, err6); prep6 != nil || prepErr != nil {
		t.Fatalf("misleading stderr fabricated native program failure: %+v %v", prep6, prepErr)
	}
}
