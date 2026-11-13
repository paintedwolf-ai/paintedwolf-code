package episode

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/pkg/api"
)

type fakeProgramReads struct {
	programs map[string]string
	pages    map[string][]api.CommandProgramReceiptPage
	err      error
}

func (f fakeProgramReads) ProgramForReceipt(_ context.Context, _, receipt string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	id, ok := f.programs[receipt]
	if !ok {
		return "", invocation.ErrProgramNotFound
	}
	return id, nil
}

func (f fakeProgramReads) ReadProgramPage(_ context.Context, _, program, cursor string, _ int) (api.CommandProgramReceiptPage, error) {
	pages := f.pages[program]
	index := 0
	if cursor != "" {
		index, _ = strconv.Atoi(cursor)
	}
	page := pages[index]
	if index+1 < len(pages) {
		page.NextCursor = strconv.Itoa(index + 1)
	}
	return page, nil
}

func settledAt() *time.Time {
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	return &at
}

func code(n int) *int { return &n }

func TestReceiptOperationsReadOutcomeFromStatusAndVerdict(t *testing.T) {
	cases := []struct {
		name    string
		receipt api.InvocationReceipt
		settle  Settlement
		outcome SemanticOutcome
		effects EffectState
		failure string
	}{
		{"completed", api.InvocationReceipt{Status: api.InvocationStatusCompleted, Invoked: true, SettledAt: settledAt()}, Settled, OutcomeSuccess, EffectsKnown, ""},
		{"failed verdict", api.InvocationReceipt{Status: api.InvocationStatusCompleted, Invoked: true, SourceVerdict: "failed", SettledAt: settledAt()}, Settled, OutcomeFailure, EffectsKnown, "source_verdict_failed"},
		{"passed verdict", api.InvocationReceipt{Status: api.InvocationStatusCompleted, Invoked: true, SourceVerdict: "passed", SettledAt: settledAt()}, Settled, OutcomeSuccess, EffectsKnown, ""},
		{"rejected before invocation", api.InvocationReceipt{Status: api.InvocationStatusRejected, Failure: &api.InvocationFailure{Code: "SCOPE_DENIED"}, SettledAt: settledAt()}, Settled, OutcomeFailure, EffectsNone, "SCOPE_DENIED"},
		{"error after invocation", api.InvocationReceipt{Status: api.InvocationStatusError, Invoked: true, SettledAt: settledAt()}, Settled, OutcomeFailure, EffectsKnown, ""},
		{"interrupted", api.InvocationReceipt{Status: api.InvocationStatusInterrupted, Invoked: true, SettledAt: settledAt()}, Settled, OutcomeUnknown, EffectsUnknown, ""},
		{"running", api.InvocationReceipt{Status: api.InvocationStatusRunning, Invoked: true}, Unsettled, OutcomeUnknown, EffectsUnknown, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.receipt.ID, tc.receipt.Tool, tc.receipt.ToolCallID = "r1", "read", "c1"
			ops, err := receiptSource{}.Operations(t.Context(), tc.receipt, "s1", "m1")
			if err != nil || len(ops) != 1 {
				t.Fatalf("operations = %+v, %v", ops, err)
			}
			op := ops[0]
			if op.Settlement != tc.settle || op.SemanticOutcome != tc.outcome || op.EffectState != tc.effects || op.FailureCode != tc.failure {
				t.Fatalf("operation = %+v", op)
			}
			if op.ID != "r1" || op.SessionID != "s1" || op.MessageID != "m1" || op.Source != "invocation-receipt" || op.SourceVerdict != tc.receipt.SourceVerdict {
				t.Fatalf("operation identity = %+v", op)
			}
		})
	}
}

func programPages(program api.CommandProgramReceipt, executions ...api.CommandExecutionReceipt) []api.CommandProgramReceiptPage {
	var pages []api.CommandProgramReceiptPage
	for _, execution := range executions {
		pages = append(pages, api.CommandProgramReceiptPage{Program: program, Executions: []api.CommandExecutionReceipt{execution}})
	}
	if len(pages) == 0 {
		pages = append(pages, api.CommandProgramReceiptPage{Program: program})
	}
	return pages
}

func programOperations(t *testing.T, program api.CommandProgramReceipt, executions ...api.CommandExecutionReceipt) []Operation {
	t.Helper()
	reads := fakeProgramReads{programs: map[string]string{"r1": "p1"}, pages: map[string][]api.CommandProgramReceiptPage{"p1": programPages(program, executions...)}}
	receipt := api.InvocationReceipt{ID: "r1", Tool: "command", ToolCallID: "c1", Status: api.InvocationStatusCompleted, Invoked: true, SettledAt: settledAt()}
	ops, err := programSource{reads}.Operations(t.Context(), receipt, "s1", "m1")
	if err != nil {
		t.Fatal(err)
	}
	return ops
}

func child(id, state string, status *int, effect string) api.CommandExecutionReceipt {
	return api.CommandExecutionReceipt{ID: id, State: state, StatusCode: status, EffectCertainty: effect, OwnerInvoked: true, NativeCommand: "cat"}
}

func TestAFailedProgramKeepsItsSettledChildrenAndTheirEffects(t *testing.T) {
	ops := programOperations(t, api.CommandProgramReceipt{State: "completed", StatusCode: code(1), StopCode: "EXIT_STATUS", SettledAt: settledAt()},
		child("e1", "completed", code(0), "completed"), child("e2", "completed", code(1), "none"))
	if len(ops) != 3 {
		t.Fatalf("operations = %+v", ops)
	}
	parent, wrote, failed := ops[0], ops[1], ops[2]
	if parent.SemanticOutcome != OutcomeFailure || parent.EffectState != EffectsKnown || parent.Settlement != Settled || parent.FailureCode != "EXIT_STATUS" {
		t.Fatalf("parent = %+v", parent)
	}
	if wrote.ParentID != "r1" || wrote.SemanticOutcome != OutcomeSuccess || wrote.EffectState != EffectsKnown || wrote.Source != "command-program" {
		t.Fatalf("child with effects = %+v", wrote)
	}
	if failed.SemanticOutcome != OutcomeFailure || failed.EffectState != EffectsNone {
		t.Fatalf("failed child = %+v", failed)
	}
}

func TestProgramSuccessNeedsSettledSucceedingChildrenAndKnownProgress(t *testing.T) {
	done := api.CommandProgramReceipt{State: "completed", StatusCode: code(0), SettledAt: settledAt()}
	if ops := programOperations(t, done, child("e1", "completed", code(0), "none")); ops[0].SemanticOutcome != OutcomeSuccess || ops[0].EffectState != EffectsNone {
		t.Fatalf("clean program = %+v", ops[0])
	}
	unknownProgress := done
	unknownProgress.ProgressState = "unknown"
	if ops := programOperations(t, unknownProgress, child("e1", "completed", code(0), "none")); ops[0].SemanticOutcome == OutcomeSuccess || ops[0].EffectState != EffectsUnknown {
		t.Fatalf("unknown progress = %+v", ops[0])
	}
	// Outcome and effects are separate facts: the work succeeded, its effect is uncertain.
	if ops := programOperations(t, done, child("e1", "completed", code(0), "attempted")); ops[0].SemanticOutcome != OutcomeSuccess || ops[0].EffectState != EffectsUnknown || ops[1].EffectState != EffectsUnknown {
		t.Fatalf("attempted effect = %+v", ops)
	}
	if ops := programOperations(t, done, child("e1", "running", nil, "none")); ops[0].Settlement != Unsettled || ops[0].SemanticOutcome == OutcomeSuccess || ops[1].Settlement != Unsettled {
		t.Fatalf("running child = %+v", ops)
	}
	running := api.CommandProgramReceipt{State: "running"}
	if ops := programOperations(t, running); ops[0].Settlement != Unsettled || ops[0].SemanticOutcome != OutcomeUnknown {
		t.Fatalf("running program = %+v", ops[0])
	}
	refused := child("e1", "refused", nil, "none")
	if ops := programOperations(t, api.CommandProgramReceipt{State: "rejected", SettledAt: settledAt()}, refused); ops[0].SemanticOutcome != OutcomeFailure || ops[1].Settlement != Settled || ops[1].SemanticOutcome != OutcomeFailure {
		t.Fatalf("refused child = %+v", ops)
	}
}

func TestOperationsJoinCallsToTheirMessagesAndUseTheProgramWhenOneExists(t *testing.T) {
	evidence := Evidence{Sessions: []Session{{
		ID:       "s1",
		Messages: []api.Message{{ID: "m1", ToolCalls: []api.ToolCall{{ID: "c1"}, {ID: "c2"}}}},
		Invocations: []api.InvocationReceipt{
			{ID: "r1", Tool: "command", ToolCallID: "c1", Status: api.InvocationStatusCompleted, Invoked: true, SettledAt: settledAt()},
			{ID: "r2", Tool: "read", ToolCallID: "c2", Status: api.InvocationStatusCompleted, Invoked: true, SettledAt: settledAt()},
		},
	}}}
	program := api.CommandProgramReceipt{State: "completed", StatusCode: code(0), SettledAt: settledAt()}
	reads := fakeProgramReads{programs: map[string]string{"r1": "p1"}, pages: map[string][]api.CommandProgramReceiptPage{"p1": programPages(program, child("e1", "completed", code(0), "none"), child("e2", "completed", code(0), "completed"))}}
	ops, err := evidence.ReadOperations(t.Context(), reads)
	if err != nil {
		t.Fatal(err)
	}
	var ids, sources []string
	for _, op := range ops {
		ids, sources = append(ids, op.ID), append(sources, op.Source)
	}
	if !slices.Equal(ids, []string{"r1", "e1", "e2", "r2"}) || !slices.Equal(sources, []string{"command-program", "command-program", "command-program", "invocation-receipt"}) {
		t.Fatalf("operations = %v %v", ids, sources)
	}
	if ops[0].MessageID != "m1" || ops[3].MessageID != "m1" || ops[0].EffectState != EffectsKnown {
		t.Fatalf("joined operations = %+v", ops)
	}
	if _, err := evidence.ReadOperations(t.Context(), fakeProgramReads{err: errors.New("store closed")}); err == nil {
		t.Fatal("a failed program read was ignored")
	}
}

func TestTheContractListsEverySourceAndClosedVocabulary(t *testing.T) {
	contract := Contract()
	if contract.Schema != ContractSchema || contract.Bundle != BundleSchema || contract.Capture != "pw-training-turn/1" || contract.Fact != "pw-training-fact/1" {
		t.Fatalf("contract identity = %+v", contract)
	}
	if !slices.Equal(contract.Sources, []SourceIdentity{{"command-program", "pw-operation-program/1"}, {"invocation-receipt", "pw-operation-receipt/1"}}) {
		t.Fatalf("sources = %+v", contract.Sources)
	}
	if !slices.Equal(contract.Files, BundleFiles) || len(contract.Settlement) != 2 || len(contract.SemanticOutcome) != 3 || len(contract.EffectState) != 3 {
		t.Fatalf("vocabulary = %+v", contract)
	}
	if !slices.Contains(contract.OfferSources, "standing") || !slices.Contains(contract.OfferSources, "companion") {
		t.Fatalf("offer sources = %v", contract.OfferSources)
	}
}
