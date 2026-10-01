package toolusage

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
)

func seedExecutionCapture(t *testing.T, capture, sessionID string) {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(capture, "store.db"))
	testutil.FailErr(t, "create execution ledger", err)
	defer func() { _ = database.Close() }()
	_, err = database.Exec(`CREATE TABLE sessions(id,parent_session_id,status);
 CREATE TABLE turns(id,status,final_output_id,active_attempt_id);
 CREATE TABLE session_turn_heads(session_id,turn_id,closeout_attempt_id);
 CREATE TABLE turn_attempt_closeouts(turn_attempt_id,closeout_json);
 CREATE TABLE prompt_submissions(id,session_id,origin,admission_seq,status);
 CREATE TABLE worker_jobs(id,parent_session_id,status,result_json);
 CREATE TABLE worker_outcome_deliveries(worker_job_id);
 CREATE TABLE checkpoints(session_id,status);
 CREATE TABLE workflow_runs(id,session_id,status,parent_run_id,created_at);
 CREATE TABLE session_model_limits(session_id,response_limit,exhausted_attempt_id);
 CREATE TABLE model_outputs(session_id,scripted);`)
	testutil.FailErr(t, "create execution tables", err)
	for _, statement := range []string{
		`INSERT INTO sessions VALUES (?,NULL,'idle')`,
		`INSERT INTO session_turn_heads VALUES (?,'turn','attempt')`,
		`INSERT INTO prompt_submissions VALUES ('submission',?,'user',1,'complete')`,
	} {
		_, err = database.Exec(statement, sessionID)
		testutil.FailErr(t, "admit session", err)
	}
	_, err = database.Exec(`INSERT INTO turns VALUES ('turn','complete','output','attempt')`)
	testutil.FailErr(t, "settle turn", err)
	closeout := store.TurnCloseout{MessageID: "message", OutputID: "output", ModelAuthored: true, Visible: true, Content: "Complete."}
	body, err := json.Marshal(closeout)
	testutil.FailErr(t, "encode closeout", err)
	_, err = database.Exec(`INSERT INTO turn_attempt_closeouts VALUES ('attempt',?)`, string(body))
	testutil.FailErr(t, "seal closeout", err)
	directory := filepath.Join(capture, "settlements", sessionID)
	testutil.FailErr(t, "create settlement receipt directory", os.MkdirAll(directory, 0o700))
	body, err = json.Marshal(session.ExecutionObservation{SessionID: sessionID, SubmissionID: "submission", SubmissionStatus: store.PromptSubmissionComplete, Settled: true})
	testutil.FailErr(t, "encode settlement receipt", err)
	testutil.FailErr(t, "retain settlement receipt", os.WriteFile(filepath.Join(directory, "submission.json"), body, 0o600))
}

func TestSettledExecutionRepairsCollectionWithoutModelReplay(t *testing.T) {
	capture := t.TempDir()
	seedExecutionCapture(t, capture, "root")
	result := CaseReport{ID: "case", SessionID: "root", Status: "error", Error: "collector disconnected"}
	testutil.FailErr(t, "collect settled execution", settleCapturedExecution(t.Context(), capture, &result))
	if result.Status != "review_required" || result.Final != "Complete." || result.Execution.OutputID != "output" {
		t.Fatalf("execution: %+v", result)
	}
	testutil.FailErr(t, "idempotent collection", settleCapturedExecution(t.Context(), capture, &result))
}

func TestHostFallbackRetainsAuthorshipForIndependentGrading(t *testing.T) {
	capture := t.TempDir()
	seedExecutionCapture(t, capture, "root")
	database, err := sql.Open("sqlite", filepath.Join(capture, "store.db"))
	testutil.FailErr(t, "open ledger", err)
	_, err = database.Exec(`UPDATE turn_attempt_closeouts SET closeout_json=json_set(closeout_json,'$.model_authored',json('false'))`)
	testutil.FailErr(t, "record host replacement", err)
	testutil.FailErr(t, "close ledger", database.Close())
	result := CaseReport{ID: "case", SessionID: "root", Status: "review_required"}
	testutil.FailErr(t, "collect host closeout", settleCapturedExecution(t.Context(), capture, &result))
	if result.Status != "review_required" || result.Final != "Complete." || result.Execution.Closeout.ModelAuthored {
		t.Fatalf("host fallback provenance lost: %+v", result)
	}
}

func TestExecutionCollectionPreservesMeasuredFailure(t *testing.T) {
	capture := t.TempDir()
	seedExecutionCapture(t, capture, "root")
	result := CaseReport{ID: "case", SessionID: "root", Status: "failed", Failure: &ExecutionFailure{Kind: "model", Code: "protected_fixture_changed"}, Error: "protected fixture changed"}
	testutil.FailErr(t, "collect failed task", settleCapturedExecution(t.Context(), capture, &result))
	if result.Status != "failed" || result.Failure == nil || result.Failure.Code != "protected_fixture_changed" {
		t.Fatalf("collection erased task failure: %+v", result)
	}
}

func TestSettledExecutionRetainsUnansweredWorkerDecision(t *testing.T) {
	capture := t.TempDir()
	seedExecutionCapture(t, capture, "root")
	database, err := sql.Open("sqlite", filepath.Join(capture, "store.db"))
	testutil.FailErr(t, "open decision ledger", err)
	defer func() { _ = database.Close() }()
	_, err = database.Exec(`INSERT INTO worker_jobs VALUES ('decision', 'root', 'held', '{"status":"needs_decision"}');
        INSERT INTO worker_outcome_deliveries VALUES ('decision')`)
	testutil.FailErr(t, "deliver worker decision", err)
	result := CaseReport{ID: "case", SessionID: "root", Status: "error", Error: "collector disconnected"}
	testutil.FailErr(t, "collect unfinished decision", settleCapturedExecution(t.Context(), capture, &result))
	if result.Status != "review_required" || result.Failure != nil {
		t.Fatalf("unanswered decision was not left to grading: %+v", result)
	}
	var status string
	testutil.FailErr(t, "retain held state", database.QueryRow(`SELECT status FROM worker_jobs WHERE id='decision'`).Scan(&status))
	if status != "held" {
		t.Fatalf("collection changed worker status: %s", status)
	}
}

func TestCaptureWithoutApplicationSettlementCannotBecomeMeasured(t *testing.T) {
	capture := t.TempDir()
	seedExecutionCapture(t, capture, "root")
	testutil.FailErr(t, "remove absent receipt", os.Remove(filepath.Join(capture, "settlements", "root", "submission.json")))
	result := CaseReport{ID: "case", SessionID: "root", Status: "error", Failure: &ExecutionFailure{Kind: "harness", Code: "execution_collection"}}
	testutil.FailErr(t, "collect incomplete capture", settleCapturedExecution(t.Context(), capture, &result))
	if result.Status != "error" || result.Failure == nil {
		t.Fatalf("inferred settlement: %+v", result)
	}
}

func TestSettlementCannotEraseConfigurationFailure(t *testing.T) {
	capture := t.TempDir()
	seedExecutionCapture(t, capture, "root")
	result := CaseReport{SessionID: "root", Status: "error", Failure: &ExecutionFailure{Kind: "harness", Code: "configuration_mismatch"}}
	testutil.FailErr(t, "collect mismatched configuration", settleCapturedExecution(t.Context(), capture, &result))
	if result.Status != "error" || result.Failure == nil || result.Failure.Code != "configuration_mismatch" {
		t.Fatalf("collection erased configuration failure: %+v", result)
	}
}

func TestCollectionRejectsUnboundSettlement(t *testing.T) {
	for _, mutation := range []string{"session", "submission", "status", "blocker", "unsettled"} {
		t.Run(mutation, func(t *testing.T) {
			capture := t.TempDir()
			seedExecutionCapture(t, capture, "root")
			observation := session.ExecutionObservation{SessionID: "root", SubmissionID: "submission", SubmissionStatus: store.PromptSubmissionComplete, Settled: true}
			switch mutation {
			case "session":
				observation.SessionID = "other"
			case "submission":
				observation.SubmissionID = "other"
			case "status":
				observation.SubmissionStatus = store.PromptSubmissionCanceled
			case "blocker":
				observation.Blockers = []session.ExecutionBlocker{{SessionID: "root", Kind: "continuation"}}
			case "unsettled":
				observation.Settled = false
			}
			body, err := json.Marshal(observation)
			testutil.FailErr(t, "encode invalid observation", err)
			testutil.FailErr(t, "retain invalid observation", os.WriteFile(filepath.Join(capture, "settlements", "root", "submission.json"), body, 0o600))
			result := CaseReport{SessionID: "root", Status: "error"}
			if err := settleCapturedExecution(t.Context(), capture, &result); err == nil || result.Status != "error" {
				t.Fatalf("invalid receipt became measurable: %+v err=%v", result, err)
			}
		})
	}
}
