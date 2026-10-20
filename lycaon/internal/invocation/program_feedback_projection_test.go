package invocation_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type feedbackMemory struct{ active, calls int }

func (m *feedbackMemory) ReserveProgramState(context.Context, int64) (func(), error) {
	m.active++
	m.calls++
	return func() { m.active-- }, nil
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
	cause := &toolrejection.ToolReject{Code: code, OwnerRef: denied.Definition().Facts().Contract.Owner, Data: map[string]any{"field": "path", "path": "input", "large": strings.Repeat("x", 40<<10)}}
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: f.session, ToolCallID: "actual-model-call"}}
	memory := &feedbackMemory{}
	prepared, err := toolexecution.PrepareProgramFailure(t.Context(), f.recorder, memory, tc, denied, cause)
	testutil.FailErr(t, "prepare actual ledger feedback", err)
	defer prepared.Close()
	if memory.active != 3 || prepared.Rejection.Code != code || prepared.Rejection.OwnerRef != cause.OwnerRef {
		t.Fatal("preparation changed owner code or lost lease")
	}
	data := prepared.Rejection.Data
	if data["tool"] != "command" || data["native_operation"] != "write" || data["execution_id"] != denied.Invocation().Identity.ExecutionID || data["model_tool_call_id"] != "actual-model-call" || data["program_executed_reads"] != 1 || data["program_replay_available"] != false {
		t.Fatalf("lost child/parent/effect facts: %+v", data)
	}
	if cause.Data["native_operation"] != nil {
		t.Fatal("supplement mutated owner rejection")
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
	testutil.FailErr(t, "retain unknown HTTP outcome", f.recorder.SettleChild(t.Context(), remote.Invocation().Identity.ExecutionID, invocation.ChildSettlement{Result: toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.EffectUnknown}, OwnerInvoked: true, DetailReference: "output:remote-unknown"}))
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
