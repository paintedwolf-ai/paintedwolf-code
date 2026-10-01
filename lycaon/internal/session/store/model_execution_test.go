package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/promptresult"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type modelExecutionStore interface {
	turnTestStore
	ConfigureModelLimit(context.Context, string, int) error
	AdmitModelResponse(context.Context, string, string) error
	SealTurnCloseout(context.Context, TurnCloseoutCommit) error
	FinishTurn(context.Context, string, string, TurnStatus, string, string, string) (Turn, error)
}

func TestModelAllowanceUsesSettledOutputsAcrossTurns(t *testing.T) {
	for _, name := range []string{"memory", "sql"} {
		t.Run(name, func(t *testing.T) {
			var st modelExecutionStore = NewMemory()
			if name == "sql" {
				st = openTurnSQLStore(t).(modelExecutionStore)
			}
			ctx := t.Context()
			sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			testutil.FailErr(t, "configure allowance", st.ConfigureModelLimit(ctx, sess.ID, 2))
			testutil.FailErr(t, "idempotent allowance", st.ConfigureModelLimit(ctx, sess.ID, 2))
			if err := st.ConfigureModelLimit(ctx, sess.ID, 3); err == nil {
				t.Fatal("allowance changed")
			}
			first, err := st.BeginTurn(ctx, TurnStart{SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginUser, InputJSON: "{}"})
			testutil.FailErr(t, "begin turn", err)
			settle := func(execution TurnExecution, iteration int, scripted bool) ModelOutput {
				t.Helper()
				testutil.FailErr(t, "admit response", st.AdmitModelResponse(ctx, sess.ID, execution.Attempt.ID))
				output, err := st.SettleModelOutput(ctx, ModelOutput{ID: uuid.NewString(), TurnAttemptID: execution.Attempt.ID, SessionID: sess.ID,
					MessageID: uuid.NewString(), Iteration: iteration, Content: "Finished", Scripted: scripted})
				testutil.FailErr(t, "settle output", err)
				_, err = st.SettleModelOutput(ctx, output)
				testutil.FailErr(t, "idempotent output settlement", err)
				return output
			}
			settle(first, 1, true)
			firstOutput := settle(first, 2, false)
			_, err = st.FinishTurn(ctx, first.Turn.ID, first.Attempt.ID, TurnStatusComplete, firstOutput.ID, "{}", "")
			testutil.FailErr(t, "finish first turn", err)
			stale := firstOutput
			stale.ID = uuid.NewString()
			stale.Iteration++
			if _, err := st.SettleModelOutput(ctx, stale); err == nil {
				t.Fatal("stale attempt committed a new output")
			}
			_, err = st.SettleModelOutput(ctx, firstOutput)
			testutil.FailErr(t, "settled output remains replayable after finalization", err)
			second, err := st.BeginTurn(ctx, TurnStart{SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginLoopWake, InputJSON: "{}"})
			testutil.FailErr(t, "begin host continuation", err)
			if err := st.AdmitModelResponse(ctx, sess.ID, first.Attempt.ID); err == nil {
				t.Fatal("old attempt admitted")
			}
			last := settle(second, 1, false)
			// Settlement of the final allowed response is valid; only another request fails.
			if err := st.AdmitModelResponse(ctx, sess.ID, second.Attempt.ID); !errors.Is(err, ErrModelResponseLimit) {
				t.Fatalf("next request = %v", err)
			}
			testutil.FailErr(t, "seal closeout", st.SealTurnCloseout(ctx, TurnCloseoutCommit{TurnID: second.Turn.ID, AttemptID: second.Attempt.ID, OutputID: last.ID, Message: &api.Message{ID: last.MessageID, Content: "Finished"}, CheckpointJSON: "{}"}))
			testutil.FailErr(t, "idempotent closeout", st.SealTurnCloseout(ctx, TurnCloseoutCommit{TurnID: second.Turn.ID, AttemptID: second.Attempt.ID, OutputID: last.ID, Message: &api.Message{ID: last.MessageID, Content: "Finished"}, CheckpointJSON: "{}"}))
			if err := st.SealTurnCloseout(ctx, TurnCloseoutCommit{TurnID: second.Turn.ID, AttemptID: second.Attempt.ID, OutputID: last.ID, Message: &api.Message{ID: last.MessageID, Content: "Changed"}, CheckpointJSON: "{}"}); err == nil {
				t.Fatal("sealed closeout changed")
			}
			if sqlStore, ok := st.(*SQL); ok {
				reopened := NewSQL(sqlStore.db)
				if err := reopened.AdmitModelResponse(ctx, sess.ID, second.Attempt.ID); !errors.Is(err, ErrModelResponseLimit) {
					t.Fatalf("reopened allowance = %v", err)
				}
			}
		})
	}
}

func TestCloseoutAuthorshipDoesNotFollowAReusedDraftSlot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		message  api.Message
		scripted bool
		want     bool
	}{
		{name: "model", message: api.Message{ID: "slot"}, want: true},
		{name: "host reuses slot", message: api.Message{ID: "slot", Origin: api.MessageOriginHost}},
		{name: "host audit", message: api.Message{ID: "slot", Grounding: &api.CitationGrounding{HostAssembled: true}}},
		{name: "new host message", message: api.Message{ID: "replacement"}},
		{name: "preparation", message: api.Message{ID: "slot"}, scripted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := closeoutRecord(tc.message, "output", "slot", tc.scripted)
			if got.ModelAuthored != tc.want {
				t.Fatalf("model authored = %v", got.ModelAuthored)
			}
		})
	}
}

func TestSQLCloseoutSurvivesHostTurnAndClearsOnNewUserAdmission(t *testing.T) {
	st := openTurnSQLStore(t).(*SQL)
	ctx := t.Context()
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	begin := func(origin TurnOrigin) TurnExecution {
		t.Helper()
		turn, err := st.BeginTurn(ctx, TurnStart{SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: origin, InputJSON: "{}"})
		testutil.FailErr(t, "admit turn", err)
		return turn
	}
	first := begin(TurnOriginUser)
	output, err := st.SettleModelOutput(ctx, ModelOutput{ID: uuid.NewString(), TurnAttemptID: first.Attempt.ID, SessionID: sess.ID, Iteration: 1, MessageID: uuid.NewString(), Content: "Complete."})
	testutil.FailErr(t, "settle model output", err)
	testutil.FailErr(t, "seal final answer", st.SealTurnCloseout(ctx, TurnCloseoutCommit{TurnID: first.Turn.ID, AttemptID: first.Attempt.ID, OutputID: output.ID, Message: &api.Message{ID: output.MessageID, Content: "Complete."}, CheckpointJSON: "{}"}))
	_, err = st.FinishTurn(ctx, first.Turn.ID, first.Attempt.ID, TurnStatusComplete, output.ID, "{}", "")
	testutil.FailErr(t, "finish user turn", err)
	host := begin(TurnOriginLoopWake)
	_, err = st.FinishTurn(ctx, host.Turn.ID, host.Attempt.ID, TurnStatusComplete, "", "{}", "")
	testutil.FailErr(t, "finish host wake", err)
	var closeout string
	err = st.db.QueryRowContext(ctx, `SELECT COALESCE(closeout_attempt_id,'') FROM session_turn_heads WHERE session_id=?`, sess.ID).Scan(&closeout)
	testutil.FailErr(t, "read preserved closeout", err)
	if closeout != first.Attempt.ID {
		t.Fatalf("host wake changed closeout: %s", closeout)
	}
	begin(TurnOriginUser)
	err = st.db.QueryRowContext(ctx, `SELECT COALESCE(closeout_attempt_id,'') FROM session_turn_heads WHERE session_id=?`, sess.ID).Scan(&closeout)
	testutil.FailErr(t, "read new task", err)
	if closeout != "" {
		t.Fatal("new user task inherited old closeout")
	}
	var retained int
	testutil.FailErr(t, "read audit retention", st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM turn_attempt_closeouts`).Scan(&retained))
	if retained != 1 {
		t.Fatal("new task deleted immutable closeout history")
	}
}

func TestSealedCloseoutRecoversWithoutRequeueingTheModel(t *testing.T) {
	for _, name := range []string{"memory", "sql"} {
		t.Run(name, func(t *testing.T) {
			var st modelExecutionStore = NewMemory()
			if name == "sql" {
				st = openTurnSQLStore(t).(modelExecutionStore)
			}
			ctx := t.Context()
			sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			submissionID := uuid.NewString()
			_, _, err = st.PutPromptSubmission(ctx, PromptSubmission{ID: submissionID, SessionID: sess.ID, ProjectID: sess.ProjectID, InputDigest: "digest", InputJSON: "{}", Origin: PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID})
			testutil.FailErr(t, "submit prompt", err)
			_, claimed, err := st.ClaimPromptSubmission(ctx, submissionID)
			testutil.FailErr(t, "claim prompt", err)
			if !claimed {
				t.Fatal("prompt not claimed")
			}
			turn, err := st.BeginTurn(ctx, TurnStart{SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginUser, InputJSON: "{}", SubmissionIDs: []string{submissionID}})
			testutil.FailErr(t, "begin turn", err)
			output, err := st.SettleModelOutput(ctx, ModelOutput{ID: uuid.NewString(), TurnAttemptID: turn.Attempt.ID, SessionID: sess.ID, Iteration: 1, MessageID: uuid.NewString(), Content: "Complete."})
			testutil.FailErr(t, "settle model output", err)
			response := promptresult.Result{MessageID: output.MessageID}
			checkpoint, err := json.Marshal(map[string]any{"final_output_id": output.ID, "result": response})
			testutil.FailErr(t, "encode finalizing checkpoint", err)
			commit := TurnCloseoutCommit{TurnID: turn.Turn.ID, AttemptID: turn.Attempt.ID, OutputID: output.ID,
				Message: &api.Message{ID: output.MessageID, Content: output.Content, Visibility: api.MessageVisibilityTranscript}, CheckpointJSON: "["}
			if err := st.SealTurnCloseout(ctx, commit); err == nil {
				t.Fatal("invalid checkpoint committed")
			}
			commit.CheckpointJSON = string(checkpoint)
			testutil.FailErr(t, "seal closeout and recovery checkpoint", st.SealTurnCloseout(ctx, commit))
			recovered, err := st.RecoverTurns(ctx)
			testutil.FailErr(t, "recover without normal turn finalization", err)
			if len(recovered) != 0 {
				t.Fatalf("completed output requeued: %+v", recovered)
			}
			submission, err := st.GetPromptSubmission(ctx, submissionID)
			testutil.FailErr(t, "read recovered submission", err)
			if submission.Status != PromptSubmissionComplete {
				t.Fatalf("submission status=%s", submission.Status)
			}
			var got promptresult.Result
			testutil.FailErr(t, "read recovered handoff", json.Unmarshal([]byte(submission.ResultJSON), &got))
			if got != response {
				t.Fatalf("recovered response=%+v want=%+v", got, response)
			}
			queued, err := st.RecoverPromptSubmissions(ctx)
			testutil.FailErr(t, "recover prompt queue", err)
			if len(queued) != 0 {
				t.Fatalf("completed prompt requeued: %v", queued)
			}
		})
	}
}

func TestCloseoutRollsBackWhenFinalizingCheckpointLosesItsClaim(t *testing.T) {
	st := openTurnSQLStore(t).(*SQL)
	ctx := t.Context()
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	turn, err := st.BeginTurn(ctx, TurnStart{SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginUser, InputJSON: "{}"})
	testutil.FailErr(t, "begin turn", err)
	_, err = st.db.ExecContext(ctx, "UPDATE turn_attempts SET status='interrupted' WHERE id=?", turn.Attempt.ID)
	testutil.FailErr(t, "invalidate attempt claim", err)
	err = st.SealTurnCloseout(ctx, TurnCloseoutCommit{TurnID: turn.Turn.ID, AttemptID: turn.Attempt.ID, Message: &api.Message{ID: "message", Content: "Complete."}, CheckpointJSON: "{}"})
	if err == nil {
		t.Fatal("closeout committed after losing its finalizing claim")
	}
	var records, heads int
	testutil.FailErr(t, "read closeout records", st.db.QueryRowContext(ctx, "SELECT count(*) FROM turn_attempt_closeouts WHERE turn_attempt_id=?", turn.Attempt.ID).Scan(&records))
	testutil.FailErr(t, "read closeout head", st.db.QueryRowContext(ctx, "SELECT count(*) FROM session_turn_heads WHERE session_id=? AND closeout_attempt_id IS NOT NULL", sess.ID).Scan(&heads))
	if records != 0 || heads != 0 {
		t.Fatalf("partial closeout committed: records=%d heads=%d", records, heads)
	}
}
