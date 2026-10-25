package invocation_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

func TestProgramDefinitionsStoredOncePerProgramAcrossLoopExecutions(t *testing.T) {
	f := newProgramReceiptFixture(t)
	firstGrep := f.childFor(t, "grep")
	secondGrep := f.childFor(t, "grep")
	thirdGrep := f.childFor(t, "grep")
	firstWrite := f.childFor(t, "write")
	secondWrite := f.childFor(t, "write")

	var defCount, execCount int
	testutil.FailErr(t, "count per-program definitions", f.database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_definitions WHERE program_id=?`, f.program).Scan(&defCount))
	testutil.FailErr(t, "count child executions", f.database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_executions WHERE program_id=?`, f.program).Scan(&execCount))
	if defCount != 2 || execCount != 5 {
		t.Fatalf("definitions=%d executions=%d, want 2 definitions for 5 executions", defCount, execCount)
	}
	var hasExecDefinitionCol int
	testutil.FailErr(t, "verify definition_json not duplicated on executions", f.database.QueryRowContext(t.Context(), `SELECT count(*) FROM pragma_table_info('invocation_program_executions') WHERE name='definition_json'`).Scan(&hasExecDefinitionCol))
	if hasExecDefinitionCol != 0 {
		t.Fatal("invocation_program_executions still duplicates definition_json")
	}
	for _, bound := range []toolcontract.BoundOperation{firstGrep, secondGrep, thirdGrep, firstWrite, secondWrite} {
		rebound, err := f.recorder.ReadProgramExecutionBinding(t.Context(), f.session, bound.Invocation().Identity.ExecutionID)
		testutil.FailErr(t, "read execution binding from deduplicated definition", err)
		if rebound.DefinitionDigest != bound.Definition().Digest() || rebound.DefinitionJSON != string(bound.Definition().Serialized()) {
			t.Fatalf("definition mismatch: got %s (%s) want %s", rebound.DefinitionDigest, rebound.DefinitionJSON, bound.Definition().Digest())
		}
	}
}

func TestProgramTraceReservesProtectTerminalSettlementUnderExhaustion(t *testing.T) {
	f := newProgramReceiptFixture(t)
	runningChild := f.childFor(t, "grep")
	heldChild := f.childFor(t, "write")
	f.start(t, runningChild)
	testutil.FailErr(t, "hold second child for approval", f.recorder.WaitChild(t.Context(), heldChild.Invocation().Identity.ExecutionID, "approval:held-write"))

	progressID := toolcontract.ProgramProgressIdentity{
		ProgramID:     f.program,
		ExecutionID:   uuid.NewString(),
		ActorID:       "fixture-person",
		SessionID:     f.session,
		RootSessionID: f.session,
		Scope:         toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope},
	}
	progressReview, releaseProgress, err := toolcontract.NewReviewedOperationInputs(t.Context(), progressMemory(t), map[string]any{"content": "- [ ] reserve"})
	testutil.FailErr(t, "freeze progress review", err)
	defer releaseProgress()
	observer := invocation.ProgramAdmissionRecorder{Recorder: f.recorder, Inputs: &retainedInputsFixture{}}
	testutil.FailErr(t, "start live progress", observer.BeforeProgressOwner(t.Context(), progressID, "actual-model-call", progressReview))

	// Active reservations: parent (32/16384) + progress (32/16384) + 2 live children (64/32768) = 128 events, 65536 bytes.
	// Exhaust non-terminal trace events up to 100000 - 128 = 99872.
	_, err = f.database.ExecContext(t.Context(), `UPDATE invocation_programs SET trace_events=100000-128 WHERE id=?`, f.program)
	testutil.FailErr(t, "set trace_events to non-terminal boundary", err)

	// New child admission must be refused because it needs events + its own 32-event reserve.
	contract, _ := toolcontract.Lookup("grep")
	def, err := toolcontract.ToolOperation("grep", json.RawMessage(`{"type":"object"}`), contract)
	testutil.FailErr(t, "freeze extra child definition", err)
	extraIn := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: f.program, NodeID: "cat-node", ExecutionID: uuid.NewString(), ParentExecutionID: f.root}, ActorID: "fixture-person", SessionID: f.session, Scope: toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope}, SourceUnitID: "submitted", Span: f.span, CatalogDigest: toolcontract.LanguageCatalogDigest()}
	expanded, releaseExpanded, err := toolcontract.NewReviewedOperationInputs(t.Context(), progressMemory(t), map[string]any{"path": "input"})
	testutil.FailErr(t, "freeze extra child inputs", err)
	defer releaseExpanded()
	if err := f.recorder.PrepareChild(t.Context(), toolcontract.ProgramChildStart{ParentReceiptID: f.parent, Invocation: extraIn, Definition: def, ArgumentsReference: "output:extra", ExpandedInputs: expanded, Canonical: "grep input"}); err == nil {
		t.Fatal("PrepareChild admitted child without settlement reserve")
	}

	// Non-terminal output attachment must also be refused when non-terminal trace events are exhausted.
	quota := &outputQuota{}
	store := f.recorder.ProgramBlobStore(blobstore.Store{Root: t.TempDir(), Dir: tooloutput.ToolOutputSpillDir})
	spill, err := tooloutput.RetainScreenedStream(t.Context(), tooloutput.ScreenedReader(io.NopCloser(bytes.NewBufferString("blocked output"))), tooloutput.StreamSpillOptions{Store: store, ClaimID: runningChild.Invocation().Identity.ExecutionID + ":blocked", MaximumBytes: 64, Admission: quota, WorkingMemory: quota, RetainedState: quota})
	testutil.FailErr(t, "retain candidate stream", err)
	if _, err := f.recorder.AttachProgramOutput(t.Context(), f.program, invocation.RetainedOutput{ExecutionID: runningChild.Invocation().Identity.ExecutionID, Stream: "stdout", Reference: "output:blocked", Complete: true}, spill, quota); err == nil {
		t.Fatal("AttachProgramOutput consumed terminal settlement reserve")
	}
	testutil.FailErr(t, "close unattached candidate stream", spill.Close())

	// Exhaust non-terminal trace bytes up to 8388608 - 65536 = 8323072 as well.
	_, err = f.database.ExecContext(t.Context(), `UPDATE invocation_programs SET trace_bytes=8388608-65536 WHERE id=?`, f.program)
	testutil.FailErr(t, "set trace_bytes to non-terminal boundary", err)

	// Started work (runningChild, heldChild, progress, and parent program) must all settle from reserved capacity.
	completed := toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect, Evidence: toolcontract.OperationEvidence{Owner: "filesystem", Kind: "result"}}
	testutil.FailErr(t, "settle running child from reserve", f.recorder.SettleChild(t.Context(), runningChild.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: completed, OwnerInvoked: true, OutputPublishable: true, DetailReference: "output:running-done"}))

	denied := toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerRefused, StopCode: "APPROVAL_DENIED", EffectCertainty: toolcontract.NoEffect}
	testutil.FailErr(t, "settle held child from reserve", f.recorder.SettleChild(t.Context(), heldChild.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: denied, OwnerInvoked: false, DetailReference: "output:approval-denied"}))

	testutil.FailErr(t, "settle live progress from reserve", f.recorder.SettleProgramProgress(t.Context(), invocation.ProgressSettlement{Identity: progressID, OwnerInvoked: true, State: "committed", DetailReference: "output:progress-done"}))

	status := 1
	testutil.FailErr(t, "settle parent program from reserve", f.recorder.SettleProgram(t.Context(), f.program, toolcontract.ProgramSettlement{State: "rejected", Status: &status, StopCode: "APPROVAL_DENIED", DetailReference: "output:program-done"}))
}

func TestProgramApprovalWaitDenialAndHeldVersionFaults(t *testing.T) {
	f := newProgramReceiptFixture(t)
	deniedChild := f.childFor(t, "write")
	staleHeldChild := f.childFor(t, "write")

	if err := f.recorder.WaitChild(t.Context(), deniedChild.Invocation().Identity.ExecutionID, ""); err == nil {
		t.Fatal("WaitChild accepted empty approval reference")
	}
	testutil.FailErr(t, "hold child for approval", f.recorder.WaitChild(t.Context(), deniedChild.Invocation().Identity.ExecutionID, "approval:deny-1"))
	if err := f.recorder.WaitChild(t.Context(), deniedChild.Invocation().Identity.ExecutionID, "approval:deny-2"); err == nil {
		t.Fatal("duplicate WaitChild transition accepted")
	}

	// Approval denial settles the waiting child without owner_started or owner_invoked.
	refused := toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerRefused, StopCode: "APPROVAL_DENIED", EffectCertainty: toolcontract.NoEffect}
	testutil.FailErr(t, "settle denied approval child", f.recorder.SettleChild(t.Context(), deniedChild.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: refused, OwnerInvoked: false, DetailReference: "output:denied"}))

	// Second child waits for approval, resumes to owner seam, and is refused on held-version conflict.
	testutil.FailErr(t, "hold stale child for approval", f.recorder.WaitChild(t.Context(), staleHeldChild.Invocation().Identity.ExecutionID, "approval:stale-1"))
	f.start(t, staleHeldChild)
	staleRefused := toolcontract.ProducerResult{
		Status:          1,
		Terminal:        toolcontract.ProducerRefused,
		StopCode:        "TEXT_WRITE_CONFLICT",
		EffectCertainty: toolcontract.NoEffect,
		Evidence:        toolcontract.OperationEvidence{Owner: "filesystem", Kind: "refusal", HeldVersionID: "held-ver-7"},
	}
	conflict := &toolcontract.RevisionConflict{Path: "src/app.go", ExpectedRevision: "rev:1", ObservedRevision: "rev:2"}
	testutil.FailErr(t, "settle held-version conflict", f.recorder.SettleChild(t.Context(), staleHeldChild.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: staleRefused, OwnerInvoked: true, DetailReference: "output:stale-conflict", AcceptedRevision: "editor:1", Conflict: conflict}))

	// A fresh invocation after conflict gets a new ExecutionID and ordinal while preserving the held execution receipt.
	freshChild := f.childFor(t, "write")
	if freshChild.Invocation().Identity.ExecutionID == staleHeldChild.Invocation().Identity.ExecutionID {
		t.Fatal("fresh invocation reused held execution identity")
	}
	page, err := f.recorder.ReadProgramPage(t.Context(), f.session, f.program, "", 10)
	testutil.FailErr(t, "read program page", err)
	if len(page.Executions) != 3 || page.Executions[0].ApprovalRef != "approval:deny-1" || page.Executions[0].OwnerStarted || page.Executions[0].OwnerInvoked || page.Executions[1].HeldVersionID != "held-ver-7" || !page.Executions[1].OwnerStarted || !page.Executions[1].OwnerInvoked || page.Executions[1].RevisionConflict == nil {
		t.Fatalf("unexpected approval/held-version execution facts: %+v", page.Executions)
	}
}

func TestProgramChildAndSettlementLedgerFaultsRollBackCleanly(t *testing.T) {
	f := newProgramReceiptFixture(t)

	// 1. Fault on node update during PrepareChild rolls back definition, execution, and trace charge.
	_, err := f.database.ExecContext(t.Context(), `CREATE TRIGGER fixture_node_fault BEFORE UPDATE ON invocation_program_nodes BEGIN SELECT RAISE(ABORT,'fixture node fault'); END`)
	testutil.FailErr(t, "install node fault trigger", err)
	var beforeEvents int64
	testutil.FailErr(t, "read trace_events before fault", f.database.QueryRowContext(t.Context(), `SELECT trace_events FROM invocation_programs WHERE id=?`, f.program).Scan(&beforeEvents))

	contract, _ := toolcontract.Lookup("grep")
	def, err := toolcontract.ToolOperation("grep", json.RawMessage(`{"type":"object"}`), contract)
	testutil.FailErr(t, "freeze grep definition", err)
	in := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: f.program, NodeID: "cat-node", ExecutionID: uuid.NewString(), ParentExecutionID: f.root}, ActorID: "fixture-person", SessionID: f.session, Scope: toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope}, SourceUnitID: "submitted", Span: f.span, CatalogDigest: toolcontract.LanguageCatalogDigest()}
	expanded, releaseExpanded, err := toolcontract.NewReviewedOperationInputs(t.Context(), progressMemory(t), map[string]any{"path": "input"})
	testutil.FailErr(t, "freeze expanded inputs", err)
	defer releaseExpanded()
	if err := f.recorder.PrepareChild(t.Context(), toolcontract.ProgramChildStart{ParentReceiptID: f.parent, Invocation: in, Definition: def, ArgumentsReference: "output:fault", ExpandedInputs: expanded, Canonical: "grep input"}); err == nil {
		t.Fatal("PrepareChild succeeded despite node fault")
	}
	_, err = f.database.ExecContext(t.Context(), `DROP TRIGGER fixture_node_fault`)
	testutil.FailErr(t, "drop node fault trigger", err)

	var afterEvents int64
	var defs, execs int
	testutil.FailErr(t, "read trace_events after rollback", f.database.QueryRowContext(t.Context(), `SELECT trace_events FROM invocation_programs WHERE id=?`, f.program).Scan(&afterEvents))
	testutil.FailErr(t, "count definitions after rollback", f.database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_definitions WHERE program_id=?`, f.program).Scan(&defs))
	testutil.FailErr(t, "count executions after rollback", f.database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_executions WHERE program_id=?`, f.program).Scan(&execs))
	if afterEvents != beforeEvents || defs != 0 || execs != 0 {
		t.Fatalf("PrepareChild rollback leaked state: events=%d->%d defs=%d execs=%d", beforeEvents, afterEvents, defs, execs)
	}

	// 2. Fault during SettleChild preserves running state and trace budget.
	child := f.child(t)
	f.start(t, child)
	_, err = f.database.ExecContext(t.Context(), `CREATE TRIGGER fixture_settle_fault BEFORE UPDATE ON invocation_program_executions WHEN NEW.state='completed' BEGIN SELECT RAISE(ABORT,'fixture settle fault'); END`)
	testutil.FailErr(t, "install settle fault trigger", err)
	completed := toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}
	if err := f.recorder.SettleChild(t.Context(), child.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: completed, OwnerInvoked: true, DetailReference: "output:done"}); err == nil {
		t.Fatal("SettleChild succeeded despite settle fault")
	}
	_, err = f.database.ExecContext(t.Context(), `DROP TRIGGER fixture_settle_fault`)
	testutil.FailErr(t, "drop settle fault trigger", err)
	testutil.FailErr(t, "SettleChild succeeds after fault cleared", f.recorder.SettleChild(t.Context(), child.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: completed, OwnerInvoked: true, DetailReference: "output:done"}))
}

type stubSourceObserver struct {
	ref string
	err error
}

func (s stubSourceObserver) CommitDeliveredSource(context.Context, string, evidence.DeliveredSourceObservation) (string, error) {
	return s.ref, s.err
}

func TestProgramDeliveryReceiptFaultPreservesOwnerAcknowledgementAndReconcilesOnRetry(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.child(t)
	quota := &outputQuota{}
	store := f.recorder.ProgramBlobStore(blobstore.Store{Root: t.TempDir(), Dir: tooloutput.ToolOutputSpillDir})
	lifetime, err := invocation.NewProgramOutputRetention(t.Context(), f.recorder, store, f.session, quota)
	testutil.FailErr(t, "admit output lifetime", err)
	testutil.FailErr(t, "restore empty output set", lifetime.Restore(t.Context()))

	const body = "alpha\nbeta\n"
	sum := sha256.Sum256([]byte(body))
	digest := hex.EncodeToString(sum[:])
	pin := toolcontract.SourcePin{SourceID: "src/input.txt", Revision: "rev:1", Representation: toolcontract.UTF8SourceBytes, SHA256: digest, StartByte: 0, EndByte: int64(len(body))}
	mapping := toolcontract.StreamSourceMapping{Source: pin, SourceStartByte: 0, SourceEndByte: int64(len(body)), OutputStartByte: 0, OutputEndByte: int64(len(body))}
	spill, err := tooloutput.RetainScreenedStream(t.Context(), tooloutput.ScreenedReader(io.NopCloser(bytes.NewBufferString(body))), tooloutput.StreamSpillOptions{Store: store, ClaimID: child.Invocation().Identity.ExecutionID + ":mapped", MaximumBytes: 1024, PreviewBytes: 64, Admission: quota, WorkingMemory: quota, RetainedState: quota})
	testutil.FailErr(t, "retain mapped stream", err)
	testutil.FailErr(t, "attach mapped stream", lifetime.Attach(t.Context(), f.program, invocation.RetainedOutput{ExecutionID: child.Invocation().Identity.ExecutionID, Stream: "stdout", Reference: "output:mapped", Complete: true, Mappings: []toolcontract.StreamSourceMapping{mapping}}, spill))

	obs, err := evidence.ConfirmDeliveredSource("src/input.txt", pin, []byte(body), []byte(body), mapping, "resp-1", "deliv-1")
	testutil.FailErr(t, "construct delivered source observation", err)

	_, err = f.database.ExecContext(t.Context(), `CREATE TRIGGER fixture_delivery_fault BEFORE INSERT ON invocation_program_deliveries BEGIN SELECT RAISE(ABORT,'fixture delivery fault'); END`)
	testutil.FailErr(t, "install delivery fault trigger", err)
	admission := feedbackAdmissionFunc(func(context.Context, int64) (func(), error) { return func() {}, nil })
	settlement, err := f.recorder.CommitProgramSourceDelivery(t.Context(), invocation.ProgramSourceDelivery{
		SessionID:       f.session,
		ProgramID:       f.program,
		OutputReference: "output:mapped",
		OutputStartByte: 0,
		OutputEndByte:   int64(len(body)),
		Observation:     obs,
	}, stubSourceObserver{ref: "evidence:owner-1"}, admission)
	if err == nil || settlement.SourceOwnerReference != "evidence:owner-1" || settlement.ReceiptRecorded {
		t.Fatalf("expected receipt fault with preserved owner ref, got %+v err=%v", settlement, err)
	}
	_, err = f.database.ExecContext(t.Context(), `DROP TRIGGER fixture_delivery_fault`)
	testutil.FailErr(t, "drop delivery fault trigger", err)

	settlement, err = f.recorder.CommitProgramSourceDelivery(t.Context(), invocation.ProgramSourceDelivery{
		SessionID:       f.session,
		ProgramID:       f.program,
		OutputReference: "output:mapped",
		OutputStartByte: 0,
		OutputEndByte:   int64(len(body)),
		Observation:     obs,
	}, stubSourceObserver{ref: "evidence:owner-1"}, admission)
	testutil.FailErr(t, "reconcile delivery receipt after fault cleared", err)
	if !settlement.ReceiptRecorded || settlement.SourceOwnerReference != "evidence:owner-1" {
		t.Fatalf("delivery reconciliation failed: %+v", settlement)
	}
}

func TestProgramFeedbackReadsRejectedProgressEnvelopeWithoutExecution(t *testing.T) {
	f := newProgramReceiptFixture(t)
	admission := feedbackAdmissionFunc(func(context.Context, int64) (func(), error) { return func() {}, nil })
	if _, err := f.recorder.ReadProgramFeedback(t.Context(), f.session, f.program, "", 2, admission); err == nil {
		t.Fatal("ReadProgramFeedback accepted empty execution before progress rejection")
	}
	testutil.FailErr(t, "record progress refusal", f.recorder.RecordProgressRefusal(t.Context(), f.program, "output:progress-refused"))
	status := 1
	testutil.FailErr(t, "settle program on progress refusal", f.recorder.SettleProgram(t.Context(), f.program, toolcontract.ProgramSettlement{State: "rejected", Status: &status, StopCode: "CLOSED_PLAN_STATE", DetailReference: "output:progress-refused"}))
	lease, err := f.recorder.ReadProgramFeedback(t.Context(), f.session, f.program, "", 2, admission)
	testutil.FailErr(t, "read progress refusal feedback without execution", err)
	defer lease.Close()
	if lease.Execution.ID != "" || lease.Projection.Program.State != "rejected" || lease.Projection.Program.ProgressState != "rejected" || lease.Projection.Program.StopCode != "CLOSED_PLAN_STATE" {
		t.Fatalf("unexpected progress refusal feedback lease: %+v", lease.Projection.Program)
	}
}
