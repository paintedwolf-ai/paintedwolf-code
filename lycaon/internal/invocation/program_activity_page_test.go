package invocation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestProgramActivitiesKeepLedgerIdentityAndSourceCoordinates(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.child(t)
	f.start(t, child)
	page, err := f.recorder.ReadProgramPage(t.Context(), f.session, f.program, "", 10)
	testutil.FailErr(t, "read activity receipt page", err)
	if len(page.Activities) != 1 || page.Activities[0].ID != child.Invocation().Identity.ExecutionID || page.Activities[0].Spans[0].SourceRef != f.sourceStart.Version.SourceReference {
		t.Fatalf("activity lost ledger identity: %+v", page.Activities)
	}
	if page.Program.ActivityRevision == 0 {
		t.Fatal("missing monotonic trace revision")
	}
	if _, err := f.recorder.ActivitySummary(t.Context(), "other-session", f.program); err == nil {
		t.Fatal("activity summary crossed session boundary")
	}
}

func TestProgramActivityObserverSeesCommittedSettlement(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.child(t)
	f.start(t, child)
	observed := 0
	ctx := invocation.WithActivityObserver(t.Context(), invocation.ActivityObserver{Execution: func(ctx context.Context, program, _ string) {
		page, err := f.recorder.ReadProgramPage(ctx, f.session, program, "", 10)
		testutil.FailErr(t, "read committed activity update", err)
		if page.Activities[0].Outcome != "Finished" {
			t.Fatalf("observed uncommitted outcome: %+v", page.Activities)
		}
		observed++
	}})
	result := toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}
	testutil.FailErr(t, "settle observed child", f.recorder.SettleChild(ctx, child.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: result, OwnerInvoked: true, DetailReference: "output:done"}))
	if observed != 1 {
		t.Fatalf("updates = %d", observed)
	}
}

func TestProgramActivityObserverSeesCommittedProgramSettlement(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.child(t)
	f.start(t, child)
	result := toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}
	testutil.FailErr(t, "settle child", f.recorder.SettleChild(t.Context(), child.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: result, OwnerInvoked: true, DetailReference: "output:done"}))
	var settled []string
	ctx := invocation.WithActivityObserver(t.Context(), invocation.ActivityObserver{Settled: func(ctx context.Context, program string) {
		summary, err := f.recorder.ActivitySummary(ctx, f.session, program)
		testutil.FailErr(t, "read settled program", err)
		settled = append(settled, summary.State)
	}})
	status := 0
	testutil.FailErr(t, "settle program", f.recorder.SettleProgram(ctx, f.program, toolcontract.ProgramSettlement{State: "completed", Status: &status, DetailReference: "output:program"}))
	if len(settled) != 1 || settled[0] != "completed" {
		t.Fatalf("settlement observations = %v", settled)
	}
}

func TestActivitySummaryCountsRepetitionsBeyondReceiptPage(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.child(t)
	f.start(t, child)
	id := child.Invocation().Identity.ExecutionID
	_, err := f.database.ExecContext(t.Context(), `UPDATE invocation_program_executions SET state='completed',status_code=0,effect_certainty='none' WHERE id=?`, id)
	testutil.FailErr(t, "settle representative", err)
	// Repeated executions share one immutable parser node.
	_, err = f.database.ExecContext(t.Context(), `WITH RECURSIVE repetitions(n) AS (SELECT 2 UNION ALL SELECT n+1 FROM repetitions WHERE n<10705)
 INSERT INTO invocation_program_executions (id,program_id,node_id,ordinal,parent_execution_id,actor_id,scope_json,operation_id,definition_digest,arguments_representation,arguments_digest,arguments_ref,canonical,state,owner,lifecycle,native_command,effect_mode,effect_classes_json,started_at,status_code,effect_certainty)
 SELECT 'repeat-'||n,program_id,node_id,n,parent_execution_id,actor_id,scope_json,operation_id,definition_digest,arguments_representation,arguments_digest,arguments_ref,canonical,'completed',owner,lifecycle,native_command,effect_mode,effect_classes_json,started_at,CASE WHEN n=10705 THEN 1 ELSE 0 END,'none'
 FROM invocation_program_executions,repetitions WHERE id=?`, id)
	testutil.FailErr(t, "record repeated executions", err)
	summary, err := f.recorder.ActivitySummary(t.Context(), f.session, f.program)
	testutil.FailErr(t, "read full activity summary", err)
	if len(summary.Activities) != 1 || summary.Activities[0].ExecutionCount != 10705 || summary.Activities[0].FailedCount != 1 || len(summary.Activities[0].ExecutionIDs) != 1 {
		t.Fatalf("summary lost later repetitions: %+v", summary.Activities)
	}
	first, err := f.recorder.ReadProgramActivityPage(t.Context(), f.session, f.program, id, "", 6)
	testutil.FailErr(t, "read activity page", err)
	if len(first.Executions) != 6 || first.ExecutionsOmitted != 10699 {
		t.Fatalf("activity paging: %+v", first)
	}
	next, err := f.recorder.ReadProgramActivityPage(t.Context(), f.session, f.program, id, first.NextCursor, 6)
	testutil.FailErr(t, "read next activity page", err)
	if len(next.Executions) != 6 || next.Executions[0].ID == first.Executions[0].ID || len(next.Activities) != 1 || next.Activities[0].ID != id {
		t.Fatal("receipt paging changed activity identity")
	}
	if _, err := f.recorder.ReadProgramActivityPage(t.Context(), f.session, f.program, "another-activity", first.NextCursor, 6); err == nil {
		t.Fatal("cursor crossed activity boundary")
	}
}

func TestActivityPageCoversFoldedRoots(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.child(t)
	f.start(t, child)
	id := child.Invocation().Identity.ExecutionID
	_, err := f.database.ExecContext(t.Context(), `UPDATE invocation_program_executions SET state='completed',status_code=0,effect_certainty='none' WHERE id=?`, id)
	testutil.FailErr(t, "settle representative", err)
	// A second same-phase root folds into the first activity's row.
	_, err = f.database.ExecContext(t.Context(), `INSERT INTO invocation_program_executions (id,program_id,node_id,ordinal,parent_execution_id,actor_id,scope_json,operation_id,definition_digest,arguments_representation,arguments_digest,arguments_ref,reviewed_arguments_ref,canonical,state,owner,lifecycle,native_command,effect_mode,effect_classes_json,started_at,status_code,effect_certainty)
 SELECT 'folded-root',program_id,'untaken-node',2,parent_execution_id,actor_id,scope_json,'grep',definition_digest,arguments_representation,arguments_digest,arguments_ref,reviewed_arguments_ref,canonical,'completed',owner,lifecycle,'grep',effect_mode,effect_classes_json,started_at,0,'none'
 FROM invocation_program_executions WHERE id=?`, id)
	testutil.FailErr(t, "record folded root", err)
	summary, err := f.recorder.ActivitySummary(t.Context(), f.session, f.program)
	testutil.FailErr(t, "read folded activity summary", err)
	if len(summary.Activities) != 1 || summary.Activities[0].ID != id || summary.Activities[0].Phase != "inspect" || len(summary.Activities[0].ExecutionIDs) != 2 {
		t.Fatalf("folded summary: %+v", summary.Activities)
	}
	page, err := f.recorder.ReadProgramActivityPage(t.Context(), f.session, f.program, id, "", 10)
	testutil.FailErr(t, "read folded activity page", err)
	if len(page.Executions) != 2 || page.Executions[0].ID != id || page.Executions[1].ID != "folded-root" {
		t.Fatalf("folded paging: %+v", page.Executions)
	}
	foldedPage, err := f.recorder.ReadProgramActivityPage(t.Context(), f.session, f.program, "folded-root", "", 10)
	testutil.FailErr(t, "read folded member activity page", err)
	if len(foldedPage.Executions) != 2 || foldedPage.Executions[0].ID != id || foldedPage.Executions[1].ID != "folded-root" {
		t.Fatalf("folded member paging: %+v", foldedPage.Executions)
	}
	if _, err := f.recorder.ReadProgramActivityPage(t.Context(), f.session, f.program, "unknown-activity", "", 10); !errors.Is(err, invocation.ErrProgramNotFound) {
		t.Fatalf("unknown activity err = %v, want ErrProgramNotFound", err)
	}
}

func TestFoldedActivityAliasStaysScopedToOwningActivity(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.child(t)
	f.start(t, child)
	id := child.Invocation().Identity.ExecutionID
	_, err := f.database.ExecContext(t.Context(), `UPDATE invocation_program_executions SET state='completed',status_code=0,effect_certainty='none' WHERE id=?`, id)
	testutil.FailErr(t, "settle representative", err)
	foldedID := uuid.NewString()
	_, err = f.database.ExecContext(t.Context(), `INSERT INTO invocation_program_executions (id,program_id,node_id,ordinal,parent_execution_id,actor_id,scope_json,operation_id,definition_digest,arguments_representation,arguments_digest,arguments_ref,reviewed_arguments_ref,canonical,state,owner,lifecycle,native_command,effect_mode,effect_classes_json,started_at,status_code,effect_certainty)
 SELECT ?,program_id,'untaken-node',2,parent_execution_id,actor_id,scope_json,'grep',definition_digest,arguments_representation,arguments_digest,arguments_ref,reviewed_arguments_ref,canonical,'completed',owner,lifecycle,'grep',effect_mode,effect_classes_json,started_at,0,'none'
 FROM invocation_program_executions WHERE id=?`, foldedID, id)
	testutil.FailErr(t, "record folded support execution", err)

	sibling := f.childFor(t, "write")
	f.start(t, sibling)
	siblingID := sibling.Invocation().Identity.ExecutionID
	writeOutcome := toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.EffectCompleted}
	testutil.FailErr(t, "settle sibling write", f.recorder.SettleChild(t.Context(), siblingID, toolcontract.ProgramChildSettlement{Result: writeOutcome, OwnerInvoked: true, DetailReference: "output:write"}))

	summary, err := f.recorder.ActivitySummary(t.Context(), f.session, f.program)
	testutil.FailErr(t, "read activity summary", err)
	if len(summary.Activities) != 2 || summary.Activities[0].ID != id || len(summary.Activities[0].ExecutionIDs) != 2 || summary.Activities[1].ID != siblingID {
		t.Fatalf("unexpected activities: %+v", summary.Activities)
	}

	otherProgram := programReceiptFixtureOn(t, f.database, f.session, "other-program-call", map[string]any{"command": "${op} arg"})
	otherChild := otherProgram.child(t)
	otherProgram.start(t, otherChild)
	otherID := otherChild.Invocation().Identity.ExecutionID

	otherSession := "session-2"
	testdbseed.InsertSession(t, f.database, otherSession, testdbseed.DefaultProjectID)
	foreignProgram := programReceiptFixtureOn(t, f.database, otherSession, "foreign-session-call", map[string]any{"command": "${op} arg"})
	foreignChild := foreignProgram.child(t)
	foreignProgram.start(t, foreignChild)
	foreignID := foreignChild.Invocation().Identity.ExecutionID

	exactPage, err := f.recorder.ReadProgramActivityPage(t.Context(), f.session, f.program, id, "", 10)
	testutil.FailErr(t, "read exact activity page", err)
	if len(exactPage.Executions) != 2 || exactPage.Executions[0].ID != id || exactPage.Executions[1].ID != foldedID {
		t.Fatalf("exact page selected wrong executions: %+v", exactPage.Executions)
	}

	// Folded support ID converges on the owning projected activity without selecting siblings.
	page1, err := f.recorder.ReadProgramActivityPage(t.Context(), f.session, f.program, foldedID, "", 1)
	testutil.FailErr(t, "read first page via folded alias", err)
	if len(page1.Executions) != 1 || page1.Executions[0].ID != id || page1.ExecutionsOmitted != 1 || page1.NextCursor == "" {
		t.Fatalf("folded page 1 = %+v", page1)
	}
	page2, err := f.recorder.ReadProgramActivityPage(t.Context(), f.session, f.program, foldedID, page1.NextCursor, 1)
	testutil.FailErr(t, "read second page via folded alias", err)
	if len(page2.Executions) != 1 || page2.Executions[0].ID != foldedID || page2.ExecutionsOmitted != 0 {
		t.Fatalf("folded page 2 = %+v", page2)
	}
	if _, err := f.recorder.ReadProgramActivityPage(t.Context(), f.session, f.program, siblingID, page1.NextCursor, 1); err == nil {
		t.Fatal("folded alias cursor crossed into sibling activity")
	}

	// Document scope using folded support ID resolves the owning projected activity and excludes siblings.
	scopes, err := invocation.ParseReceiptScopes([]string{f.program + ":" + foldedID})
	testutil.FailErr(t, "parse folded document scope", err)
	doc := readReceipt(t, f, scopes, 0, 64)
	records := decodeReceiptRows(t, doc.Rows)
	if len(records) != 1+2+2 || records[0].Type != "program" || records[0].Activity == nil || records[0].Activity.ID != id || records[0].ExecutionCount != 2 {
		t.Fatalf("folded document header = %+v (rows=%d)", records[0], len(records))
	}
	if records[1].Execution.ID != id || records[2].Execution.ID != foldedID || records[3].Type != "node" {
		t.Fatalf("folded document rows included unexpected executions: %+v", records[1:4])
	}

	// Unknown, cross-program, and cross-session IDs fail with ErrProgramNotFound.
	unknownID := uuid.NewString()
	for _, tc := range []struct {
		name, session, activityID string
	}{
		{"unknown", f.session, unknownID},
		{"cross-program same session", f.session, otherID},
		{"cross-session foreign execution", f.session, foreignID},
		{"cross-session folded alias", otherSession, foldedID},
	} {
		if _, err := f.recorder.ReadProgramActivityPage(t.Context(), tc.session, f.program, tc.activityID, "", 10); !errors.Is(err, invocation.ErrProgramNotFound) {
			t.Fatalf("page(%s) err = %v, want ErrProgramNotFound", tc.name, err)
		}
		if _, err := f.recorder.ReadReceiptDocument(t.Context(), tc.session, []invocation.ReceiptScope{{ProgramID: f.program, ActivityID: tc.activityID}}, "", 0, 64); !errors.Is(err, invocation.ErrProgramNotFound) {
			t.Fatalf("document(%s) err = %v, want ErrProgramNotFound", tc.name, err)
		}
	}
}
