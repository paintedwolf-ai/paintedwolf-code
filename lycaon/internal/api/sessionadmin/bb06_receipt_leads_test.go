package sessionadmin_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/activitybriefing"
	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (f *bb06Fixture) publishAndCaptureActivity(t *testing.T, sub <-chan wire.EventEnvelope, callID, status string, program wire.CommandProgramReceipt) (wire.EventEnvelope, wire.ActivityEvent) {
	t.Helper()
	f.pub.PublishActivity(t.Context(), f.project.ID, f.session.ID, wire.ActivityEvent{
		SessionID:  f.session.ID,
		ToolCallID: callID,
		ToolName:   "command",
		ActivityID: "lease-" + callID,
		Kind:       "running_tool",
		Status:     wire.ActivityStatus(status),
		StartedAt:  time.Now().UTC(),
		Progress: &wire.ToolProgress{
			Phase:          "executing",
			CommandProgram: &program,
		},
	})
	select {
	case env := <-sub:
		var ev wire.ActivityEvent
		testutil.FailErr(t, "decode captured activity event", json.Unmarshal(env.Data, &ev))
		return env, ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for published activity event envelope")
		return wire.EventEnvelope{}, wire.ActivityEvent{}
	}
}

func activityIDsOf(activities []wire.CommandActivity) []string {
	out := make([]string, len(activities))
	for i, a := range activities {
		out[i] = a.ID
	}
	return out
}

// TestBB06_L02_L06_PipelineReceiptLookupCorrelation implements the BB06
// receipt-lookup correlation matrix (coordination/decisions/BB06-receipt-lookup-evidence.md):
// captures the full SSE event envelope and activity projection before and after
// terminal settlement, compares the emitted receipt key against both the
// unscoped program and projected activity IDs, and probes program-only, emitted
// activity scope, known-invalid scope, receipt-document, and briefing routes on
// the same program/session identity.
func TestBB06_L02_L06_PipelineReceiptLookupCorrelation(t *testing.T) {
	f := newBB06Fixture(t)
	sub, unsub, err := f.hub.Subscribe(t.Context(), events.Subscription{Project: f.project.ID})
	testutil.FailErr(t, "subscribe event hub", err)
	defer unsub()

	p := f.beginProgram(t, "call-l02-l06-pipe", "head | external-tool", "pipe-node", "read-node", "cmd-node")
	f.causal.calls[p.callID] = briefing.CausalRead{
		Found: true,
		Call:  briefing.CallExcerpt{Name: "command", Args: `{"command":"head | external-tool"}`},
	}
	pipeID := uuid.NewString()
	readID := uuid.NewString()
	cmdID := uuid.NewString()
	knownInvalidID := uuid.NewString()

	// Stage 1 + Stage 2 (read sub-execution enters waiting/fallback before stage 3).
	f.insertExecution(t, p, seededExec{
		id: pipeID, nodeID: "pipe-node", ordinal: 1, parentExecID: p.rootID,
		operationID: "program |", state: "running", owner: "program",
		lifecycle: "read_only", nativeCommand: "program |", effectMode: "read_only",
		effectClasses: `["read_only"]`, canonical: "head | external-tool", ownerStarted: 1,
	})
	f.insertExecution(t, p, seededExec{
		id: readID, nodeID: "read-node", ordinal: 2, parentExecID: pipeID,
		operationID: "read", state: "waiting", owner: "filesystem",
		lifecycle: "read_only", nativeCommand: "head", effectMode: "read_only",
		effectClasses: `["read_only"]`, canonical: "head", ownerStarted: 1,
	})

	// Capture full live event envelope and projection before settlement.
	liveSummary, err := f.recorder.LiveActivitySummary(t.Context(), f.session.ID, p.programID)
	testutil.FailErr(t, "read live activity summary", err)
	liveEnv, liveEv := f.publishAndCaptureActivity(t, sub, p.callID, "active", liveSummary)
	if liveEv.Progress == nil || liveEv.Progress.CommandProgram == nil || len(liveEv.Progress.CommandProgram.Activities) != 1 {
		t.Fatalf("unexpected live envelope projection: %+v", liveEv)
	}
	liveAct := liveEv.Progress.CommandProgram.Activities[0]
	if liveAct.Action == nil || liveAct.Action.Preview == nil || len(liveAct.Action.Preview.Receipts) != 1 {
		t.Fatalf("missing live preview.receipts in SSE envelope %s: %+v", liveEnv.EventID, liveAct.Action)
	}
	emitted := liveAct.Action.Preview.Receipts[0]
	t.Logf("LIVE envelope=%s program=%s projected_activities=%v emitted_receipt={program_id:%s activity_id:%s}",
		liveEnv.EventID, p.programID, activityIDsOf(liveEv.Progress.CommandProgram.Activities), emitted.ProgramID, emitted.ActivityID)

	// Probe live: (a) program-only, (b) emitted scope, (c) known-invalid scope, (d) receipt-document.
	if w := f.get(fmt.Sprintf("/v1/sessions/%s/program-receipts/%s?limit=6", f.session.ID, p.programID)); w.Code != http.StatusOK {
		t.Fatalf("live program-only status = %d: %s", w.Code, w.Body.String())
	}
	if w := f.get(fmt.Sprintf("/v1/sessions/%s/program-receipts/%s?limit=6&activity_id=%s", f.session.ID, emitted.ProgramID, emitted.ActivityID)); w.Code != http.StatusOK {
		t.Fatalf("live emitted scope status = %d: %s", w.Code, w.Body.String())
	}
	if w := f.get(fmt.Sprintf("/v1/sessions/%s/program-receipts/%s?limit=6&activity_id=%s", f.session.ID, p.programID, knownInvalidID)); w.Code != http.StatusNotFound {
		t.Fatalf("live known-invalid scope status = %d, want 404: %s", w.Code, w.Body.String())
	}
	if w := f.get(fmt.Sprintf("/v1/sessions/%s/program-receipt-document?scopes=%s:%s&offset=0&limit=64", f.session.ID, emitted.ProgramID, emitted.ActivityID)); w.Code != http.StatusOK {
		t.Fatalf("live receipt-document emitted scope status = %d: %s", w.Code, w.Body.String())
	}

	// Stage 3 arrives (command, owner=processes) and program settles terminally.
	zero := 0
	_, err = f.db.ExecContext(t.Context(), `UPDATE invocation_program_executions SET state='completed', status_code=0 WHERE id IN (?, ?)`, pipeID, readID)
	testutil.FailErr(t, "complete pipe and read", err)
	f.insertExecution(t, p, seededExec{
		id: cmdID, nodeID: "cmd-node", ordinal: 3, parentExecID: pipeID,
		operationID: "command", state: "completed", owner: "processes",
		lifecycle: "effect_attempt", nativeCommand: "external", effectMode: "external",
		effectClasses: `["external"]`, canonical: "external-tool", statusCode: &zero, ownerStarted: 1,
	})
	testutil.FailErr(t, "settle pipeline program", f.recorder.SettleProgram(t.Context(), p.programID, toolcontract.ProgramSettlement{
		State: "completed", Status: &zero, DetailReference: "output:pipe-done",
	}))

	// Capture full post-settlement event envelope and compare projected activity IDs.
	settledSummary, err := f.recorder.ActivitySummary(t.Context(), f.session.ID, p.programID)
	testutil.FailErr(t, "read settled activity summary", err)
	settledEnv, settledEv := f.publishAndCaptureActivity(t, sub, p.callID, "done", settledSummary)
	settledIDs := activityIDsOf(settledEv.Progress.CommandProgram.Activities)
	t.Logf("SETTLED envelope=%s program=%s projected_activities=%v emitted_live_activity_id=%s",
		settledEnv.EventID, p.programID, settledIDs, emitted.ActivityID)

	// (a) Unscoped program-only read proves the program row IS committed and available.
	unscoped := f.get(fmt.Sprintf("/v1/sessions/%s/program-receipts/%s?limit=6", f.session.ID, p.programID))
	if unscoped.Code != http.StatusOK {
		t.Fatalf("settled program-only status = %d, want 200: %s", unscoped.Code, unscoped.Body.String())
	}
	var unscopedPage wire.CommandProgramReceiptPage
	testutil.FailErr(t, "decode unscoped settled page", json.Unmarshal(unscoped.Body.Bytes(), &unscopedPage))
	if len(unscopedPage.Activities) != 1 || unscopedPage.Activities[0].ID != cmdID {
		t.Fatalf("expected settled projected activity %s, got %+v", cmdID, unscopedPage.Activities)
	}

	// (b) Emitted activity scope (stale/folded readID) on committed program.
	emittedReceipt := f.get(fmt.Sprintf("/v1/sessions/%s/program-receipts/%s?limit=6&activity_id=%s", f.session.ID, emitted.ProgramID, emitted.ActivityID))
	// (c) Known-invalid activity scope on the same committed program.
	invalidReceipt := f.get(fmt.Sprintf("/v1/sessions/%s/program-receipts/%s?limit=6&activity_id=%s", f.session.ID, p.programID, knownInvalidID))
	// (d) Receipt-document route for emitted scope and known-invalid scope.
	emittedDoc := f.get(fmt.Sprintf("/v1/sessions/%s/program-receipt-document?scopes=%s:%s&offset=0&limit=64", f.session.ID, emitted.ProgramID, emitted.ActivityID))
	invalidDoc := f.get(fmt.Sprintf("/v1/sessions/%s/program-receipt-document?scopes=%s:%s&offset=0&limit=64", f.session.ID, p.programID, knownInvalidID))

	t.Logf("POST-SETTLEMENT PROBE program_only=%d emitted_receipt=%d invalid_receipt=%d emitted_doc=%d invalid_doc=%d",
		unscoped.Code, emittedReceipt.Code, invalidReceipt.Code, emittedDoc.Code, invalidDoc.Code)
	if invalidReceipt.Code != http.StatusNotFound || invalidDoc.Code != http.StatusNotFound {
		t.Fatalf("known-invalid scope expected 404: receipt=%d doc=%d", invalidReceipt.Code, invalidDoc.Code)
	}
	if emittedReceipt.Code != http.StatusOK {
		t.Errorf("DEFECT L02 REPRODUCED: committed program=%s (program-only=200, projected=%v) returned %d for SSE-emitted folded read activity_id=%s: %s",
			p.programID, settledIDs, emittedReceipt.Code, emitted.ActivityID, emittedReceipt.Body.String())
	}
	if emittedDoc.Code != http.StatusOK {
		t.Errorf("DEFECT L02 REPRODUCED: committed program=%s receipt-document returned %d for SSE-emitted folded read scope=%s:%s: %s",
			p.programID, emittedDoc.Code, emitted.ProgramID, emitted.ActivityID, emittedDoc.Body.String())
	}

	// (e) Briefing route probes on the same session/program identity.
	briefingReq := func(programID, activityID string) map[string]any {
		return map[string]any{
			"profile_id": activitybriefing.ProfileID,
			"trigger":    "manual",
			"target_ref": wire.ToolActivityBriefingTarget{
				SessionID:   f.session.ID,
				ToolCallIds: []string{p.callID},
				Members:     []wire.ToolActivityBriefingMember{{ProgramID: programID, ActivityID: activityID}},
			},
		}
	}

	t.Run("briefing_valid_projected_activity", func(t *testing.T) {
		resp := f.postJSON("/v1/projects/"+f.project.ID+"/briefings", briefingReq(p.programID, cmdID))
		if resp.Code != http.StatusOK {
			t.Fatalf("valid projected briefing status = %d, want 200: %s", resp.Code, resp.Body.String())
		}
	})

	for _, tc := range []struct {
		name, programID, activityID string
	}{
		{"briefing_emitted_folded_read_scope", emitted.ProgramID, emitted.ActivityID},
		{"briefing_known_invalid_activity_scope", p.programID, knownInvalidID},
		{"briefing_missing_program", uuid.NewString(), knownInvalidID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.postJSON("/v1/projects/"+f.project.ID+"/briefings", briefingReq(tc.programID, tc.activityID))
			if resp.Code == http.StatusInternalServerError {
				t.Errorf("DEFECT L06 REPRODUCED (%s): POST /v1/projects/%s/briefings returned 500 internal_error instead of typed 404/409: %s",
					tc.name, f.project.ID, resp.Body.String())
			}
		})
	}
}

// TestBB06_L07_SourceCommandWindowsReadOnlyVsMutating verifies backend command
// window retention: read-only windows without operations or coverage gaps are
// intentionally pruned (returning 404 source_history_not_found), while mutating
// or gapped windows remain readable (200 OK).
func TestBB06_L07_SourceCommandWindowsReadOnlyVsMutating(t *testing.T) {
	f := newBB06Fixture(t)
	q := db.New(f.db)

	readOnlyWin := uuid.NewString()
	mutatingWin := uuid.NewString()
	gappedWin := uuid.NewString()

	for i, id := range []string{readOnlyWin, mutatingWin, gappedWin} {
		testutil.FailErr(t, "insert window", q.InsertSourceCommandWindow(t.Context(), db.InsertSourceCommandWindowParams{
			ID: id, ProjectID: f.project.ID, SessionID: f.session.ID,
			ToolName: "command", Ordinal: int64(i + 1), StartedTs: "2026-11-26T10:00:00Z",
		}))
		testutil.FailErr(t, "end window", q.EndSourceCommandWindow(t.Context(), db.EndSourceCommandWindowParams{
			ID: id, EndedTs: "2026-11-26T10:00:01Z",
		}))
	}

	_, err := f.db.ExecContext(t.Context(), `INSERT INTO source_operations (
		id, project_id, branch_id, origin, person_id, cause, actor_label, session_id,
		job_id, turn, tool_call_id, tool_name, batch_id, operation_key, capture_quality,
		started_ts, committed_ts, command_window_id
	) VALUES (?, ?, '', 'agent', NULL, 'command', 'agent', ?, '', 1, 'call-mut', 'command', '', ?, 'exact',
		'2026-11-26T10:00:00Z', '2026-11-26T10:00:01Z', ?)`,
		uuid.NewString(), f.project.ID, f.session.ID, uuid.NewString(), mutatingWin)
	testutil.FailErr(t, "insert mutating source_operation", err)

	testutil.FailErr(t, "record coverage gap", q.UpsertSourceCommandWindowCoverage(t.Context(), db.UpsertSourceCommandWindowCoverageParams{
		WindowID: gappedWin, RootID: f.project.Outside.ID, Path: "srv/shared",
		Coverage: "not_observed", Reason: "io_error", BoundariesJson: "[]",
	}))

	prunedRO, err := q.DeleteSourceCommandWindowIfUnreferenced(t.Context(), readOnlyWin)
	testutil.FailErr(t, "prune read-only window", err)
	prunedMut, err := q.DeleteSourceCommandWindowIfUnreferenced(t.Context(), mutatingWin)
	testutil.FailErr(t, "prune mutating window", err)
	prunedGap, err := q.DeleteSourceCommandWindowIfUnreferenced(t.Context(), gappedWin)
	testutil.FailErr(t, "prune gapped window", err)
	if prunedRO != 1 || prunedMut != 0 || prunedGap != 0 {
		t.Fatalf("pruned counts = (ro=%d, mut=%d, gap=%d), want (1, 0, 0)", prunedRO, prunedMut, prunedGap)
	}

	roResp := f.get(fmt.Sprintf("/v1/projects/%s/source/command-windows/%s?session_id=%s", f.project.ID, readOnlyWin, f.session.ID))
	if roResp.Code != http.StatusNotFound {
		t.Fatalf("pruned read-only window status = %d, want 404: %s", roResp.Code, roResp.Body.String())
	}
	var roErr wire.ErrorResponse
	testutil.FailErr(t, "decode read-only 404", json.Unmarshal(roResp.Body.Bytes(), &roErr))
	if roErr.Code != wire.ApiErrorCodeSourceHistoryNotFound {
		t.Fatalf("pruned read-only window code = %q, want %q", roErr.Code, wire.ApiErrorCodeSourceHistoryNotFound)
	}

	mutResp := f.get(fmt.Sprintf("/v1/projects/%s/source/command-windows/%s?session_id=%s", f.project.ID, mutatingWin, f.session.ID))
	if mutResp.Code != http.StatusOK {
		t.Fatalf("mutating window status = %d, want 200: %s", mutResp.Code, mutResp.Body.String())
	}
	var mutState wire.CommandObservationStatus
	testutil.FailErr(t, "decode mutating state", json.Unmarshal(mutResp.Body.Bytes(), &mutState))
	if mutState.Settling || len(mutState.Coverage) != 0 {
		t.Fatalf("mutating observation = %+v", mutState)
	}

	gapResp := f.get(fmt.Sprintf("/v1/projects/%s/source/command-windows/%s?session_id=%s", f.project.ID, gappedWin, f.session.ID))
	if gapResp.Code != http.StatusOK {
		t.Fatalf("gapped window status = %d, want 200: %s", gapResp.Code, gapResp.Body.String())
	}
	var gapState wire.CommandObservationStatus
	testutil.FailErr(t, "decode gapped state", json.Unmarshal(gapResp.Body.Bytes(), &gapState))
	if gapState.Settling || len(gapState.Coverage) != 1 || gapState.Coverage[0].Reason != wire.SourceWindowGapIoError {
		t.Fatalf("gapped observation = %+v", gapState)
	}
}
