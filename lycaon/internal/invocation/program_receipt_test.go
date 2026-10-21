package invocation_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

var _ commandprogram.ProgramRecorder = (*invocation.SQLRecorder)(nil)

type programReceiptFixture struct {
	recorder                       *invocation.SQLRecorder
	database                       db.Handle
	parent, program, root, session string
	definition                     toolcontract.OperationDefinition
	span                           toolcontract.SourceSpan
	sourceStart                    toolcontract.ProgramStart
}

func newProgramReceiptFixture(t *testing.T) programReceiptFixture {
	t.Helper()
	return newProgramReceiptFixtureWithArgs(t, map[string]any{"command": "${op} arg"})
}

func newProgramReceiptFixtureWithArgs(t *testing.T, args map[string]any) programReceiptFixture {
	t.Helper()
	database := testdbfixture.Open(t, "programs.db")
	session := "session-1"
	testdbseed.InsertSession(t, database, session, testdbseed.DefaultProjectID)
	recorder := invocation.NewSQLRecorder(database)
	contract, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("command contract missing")
	}
	parent, err := recorder.Begin(t.Context(), invocation.Start{ProjectID: testdbseed.DefaultProjectID, SessionID: session, ToolCallID: "actual-model-call", ToolName: "command", Args: args, Contract: contract})
	testutil.FailErr(t, "begin real model call", err)
	read, _ := toolcontract.Lookup("read")
	definition, err := toolcontract.ToolOperation("read", json.RawMessage(`{"type":"object"}`), read)
	testutil.FailErr(t, "freeze native definition", err)
	f := programReceiptFixture{recorder: recorder, database: database, parent: parent.ID, program: uuid.NewString(), root: uuid.NewString(), session: session, definition: definition, span: toolcontract.SourceSpan{StartByte: 0, EndByte: 9, Line: 1, Column: 1}}
	digest := sha256.Sum256([]byte("${op} arg"))
	sourceDigest := hex.EncodeToString(digest[:])
	f.sourceStart = toolcontract.ProgramStart{Version: toolcontract.ProgramVersion{ProgramID: f.program, ParentReceiptID: f.parent, RootExecutionID: f.root, LanguageVersion: toolcontract.LanguageVersion, CatalogDigest: toolcontract.LanguageCatalogDigest(), SourceUnitID: "submitted", SourceDigest: sourceDigest, SourceReference: "output:exact-program"}, PlanReference: "output:validated-plan", SourceExact: true, SyntaxValidated: true, Sources: []toolcontract.ProgramSource{{UnitID: "submitted", Pin: toolcontract.SourcePin{SourceID: "submitted-program", Revision: sourceDigest, Representation: toolcontract.UTF8SourceBytes, SHA256: sourceDigest, EndByte: 9}, DetailReference: "output:exact-program"}}, Nodes: []toolcontract.ProgramNode{{ID: "cat-node", SourceUnitID: "submitted", Span: f.span}, {ID: "untaken-node", SourceUnitID: "submitted", Span: f.span}}}
	err = recorder.BeginProgram(t.Context(), f.sourceStart)
	testutil.FailErr(t, "attach validated program", err)
	return f
}

func (f programReceiptFixture) child(t *testing.T) toolcontract.BoundOperation {
	t.Helper()
	return f.childFor(t, "grep")
}

func (f programReceiptFixture) childFor(t *testing.T, name string) toolcontract.BoundOperation {
	t.Helper()
	contract, ok := toolcontract.Lookup(name)
	if !ok {
		t.Fatal("fixture contract missing")
	}
	definition, err := toolcontract.ToolOperation(name, json.RawMessage(`{"type":"object"}`), contract)
	testutil.FailErr(t, "freeze operation", err)
	in := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: f.program, NodeID: "cat-node", ExecutionID: uuid.NewString(), ParentExecutionID: f.root}, ActorID: "fixture-person", SessionID: f.session, Scope: toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope}, SourceUnitID: "submitted", Span: f.span, CatalogDigest: toolcontract.LanguageCatalogDigest()}
	expanded, release, err := toolcontract.NewReviewedOperationInputs(t.Context(), progressMemory(t), map[string]any{"path": "input"})
	testutil.FailErr(t, "freeze lossless expanded arguments", err)
	defer release()
	testutil.FailErr(t, "prepare dynamic child", f.recorder.PrepareChild(t.Context(), toolcontract.ProgramChildStart{ParentReceiptID: f.parent, Invocation: in, Definition: definition, ArgumentsReference: "output:expanded-input", ExpandedInputs: expanded, Canonical: "cat input"}))
	bound, err := toolcontract.BindOperation(definition, in, definition.Facts().Commands[0].Name, definition.Facts().Commands[0].Effects[0].Mode, nil)
	testutil.FailErr(t, "bind actual native operation", err)
	return bound
}

type retainedInputsFixture struct{ calls int }

func (r *retainedInputsFixture) RetainReviewedProgramInputs(_ context.Context, _ toolcontract.OperationInvocation, _ toolcontract.ReviewedOperationInputs) (string, error) {
	r.calls++
	return "output:screened-review", nil
}

func (f programReceiptFixture) start(t *testing.T, bound toolcontract.BoundOperation) {
	t.Helper()
	inputs := &retainedInputsFixture{}
	review, release, err := toolcontract.NewReviewedOperationInputs(t.Context(), progressMemory(t), map[string]any{"path": "input"})
	testutil.FailErr(t, "freeze actual lossless reviewed inputs", err)
	defer release()
	observer := invocation.ProgramAdmissionRecorder{Recorder: f.recorder, Inputs: inputs}
	testutil.FailErr(t, "record before owner", observer.BeforeNativeOwner(t.Context(), bound, "actual-model-call", review))
	if inputs.calls != 1 {
		t.Fatalf("review retention calls=%d", inputs.calls)
	}
}

func TestProgramLoopIterationsHaveIndependentSettlementAndHeldIdentity(t *testing.T) {
	f := newProgramReceiptFixture(t)
	first := f.child(t)
	second := f.child(t)
	testutil.FailErr(t, "hold first child", f.recorder.WaitChild(t.Context(), first.Invocation().Identity.ExecutionID, "approval:one"))
	f.start(t, first)
	f.start(t, second)
	result := toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, PublicationBasis: toolcontract.NativeComparisonPublication, EffectCertainty: toolcontract.NoEffect, Evidence: toolcontract.OperationEvidence{Owner: "filesystem", Kind: "result"}}
	for _, bound := range []toolcontract.BoundOperation{first, second} {
		err := f.recorder.SettleChild(t.Context(), bound.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: result, OwnerInvoked: true, OutputPublishable: true, DetailReference: "output:complete-result"})
		testutil.FailErr(t, "settle independent iteration", err)
	}
	if err := f.recorder.SettleChild(t.Context(), first.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: result, OwnerInvoked: true, OutputPublishable: true, DetailReference: "output:duplicate"}); err == nil {
		t.Fatal("duplicate settlement accepted")
	}
	page, err := f.recorder.ReadProgramPage(t.Context(), f.session, f.program, "", 1)
	testutil.FailErr(t, "read bounded first page", err)
	if len(page.Executions) != 1 || page.ExecutionsOmitted != 1 || page.NextCursor == "" || page.Executions[0].StatusCode == nil || *page.Executions[0].StatusCode != 1 || !page.Executions[0].OutputPublishable {
		t.Fatalf("lost first iteration: %+v", page)
	}
	next, err := f.recorder.ReadProgramPage(t.Context(), f.session, f.program, page.NextCursor, 1)
	testutil.FailErr(t, "read second independent iteration", err)
	if len(next.Executions) != 1 || next.Executions[0].ID == page.Executions[0].ID || next.Executions[0].NodeID != page.Executions[0].NodeID {
		t.Fatalf("iteration identity lost: %+v", next)
	}
	if _, err = f.recorder.ReadProgramPage(t.Context(), "another-session", f.program, page.NextCursor, 1); err == nil {
		t.Fatal("cursor crossed session scope")
	}
}

func TestProgramHostLossKeepsCompletedWriteAndUnknownAttemptSeparate(t *testing.T) {
	f := newProgramReceiptFixture(t)
	write := f.childFor(t, "write")
	unknown := f.childFor(t, "http_request")
	f.start(t, write)
	f.start(t, unknown)
	outcome := toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.EffectCompleted, Evidence: toolcontract.OperationEvidence{Owner: "filesystem", Kind: "journal", OwnerRef: "journal:fixture-write", RecoveryRef: "journal:fixture-write"}}
	testutil.FailErr(t, "retain completed owner outcome", f.recorder.SettleChild(t.Context(), write.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: outcome, OwnerInvoked: true, DetailReference: "output:write-result", AcceptedRevision: "editor:2", PublishedRevision: "disk:2"}))
	count, err := f.recorder.InterruptPrograms(t.Context(), f.session, "output:host-loss")
	testutil.FailErr(t, "interrupt without replay", err)
	if count != 1 {
		t.Fatalf("interrupted %d programs", count)
	}
	page, err := f.recorder.ReadProgramPage(t.Context(), f.session, f.program, "", 20)
	testutil.FailErr(t, "inspect owner outcomes after host loss", err)
	if len(page.Executions) != 2 || page.Executions[0].EffectCertainty != "completed" || page.Executions[0].RecoveryRef != "journal:fixture-write" || page.Executions[1].EffectCertainty != "unknown" || page.Executions[1].StatusCode != nil || page.Nodes[1].State != "not_started" {
		t.Fatalf("host loss rewrote known/unknown/not-started facts: %+v", page)
	}
}
