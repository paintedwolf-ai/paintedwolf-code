package store

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type turnTestStore interface {
	Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
	Get(context.Context, string) (*api.Session, error)
	SetSessionStatus(context.Context, string, api.SessionStatus) error
	PutPromptSubmission(context.Context, PromptSubmission) (*PromptSubmission, bool, error)
	ClaimPromptSubmission(context.Context, string) (*PromptSubmission, bool, error)
	GetPromptSubmission(context.Context, string) (*PromptSubmission, error)
	RecoverPromptSubmissions(context.Context) ([]string, error)
	BeginTurn(context.Context, TurnStart) (TurnExecution, error)
	CheckpointTurn(context.Context, string, string, TurnPhase, string) error
	RecoverTurns(context.Context) ([]Turn, error)
	RecoverTurnsForSession(context.Context, string) ([]Turn, error)
	ProjectLiveModelOutput(context.Context, LiveModelOutput) error
	SettleModelOutput(context.Context, ModelOutput) (ModelOutput, error)
	ListPendingModelOutputProjections(context.Context) ([]PendingModelOutputProjection, error)
	AppendMessages(context.Context, string, ...api.Message) error
	MarkModelOutputProjected(context.Context, string) error
	TruncateMessagesFrom(context.Context, string, string) (int, error)
}

func TestTurnAttemptRecoveryParity(t *testing.T) {
	for _, fixture := range []struct {
		name string
		open func(*testing.T) turnTestStore
	}{
		{name: "memory", open: func(*testing.T) turnTestStore { return NewMemory() }},
		{name: "sql", open: openTurnSQLStore},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := t.Context()
			st := fixture.open(t)
			sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			submissionID := uuid.NewString()
			_, _, err = st.PutPromptSubmission(ctx, PromptSubmission{
				ID: submissionID, SessionID: sess.ID, ProjectID: sess.ProjectID,
				InputDigest: "digest", InputJSON: `{"text":"resume me"}`, Origin: PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID,
			})
			testutil.FailErr(t, "put submission", err)

			first, err := st.BeginTurn(ctx, TurnStart{
				SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginUser,
				InputJSON: `{"text":"resume me"}`, SubmissionIDs: []string{submissionID},
			})
			testutil.FailErr(t, "begin first attempt", err)
			if first.Attempt.Attempt != 1 {
				t.Fatalf("first attempt = %d", first.Attempt.Attempt)
			}

			recovered, err := st.RecoverTurns(ctx)
			testutil.FailErr(t, "recover turns", err)
			if len(recovered) != 1 || recovered[0].ID != first.Turn.ID {
				t.Fatalf("recovered = %+v", recovered)
			}
			second, err := st.BeginTurn(ctx, TurnStart{
				SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginUser,
				InputJSON: `{"text":"resume me"}`, SubmissionIDs: []string{submissionID},
			})
			testutil.FailErr(t, "begin recovered attempt", err)
			if second.Turn.ID != first.Turn.ID || second.Attempt.Attempt != 2 || second.Attempt.ID == first.Attempt.ID {
				t.Fatalf("second execution = %+v, first = %+v", second, first)
			}
		})
	}
}

// Live recovery leaves other sessions' running turns untouched.
func TestRecoverTurnsForSessionLeavesOtherSessionsAloneParity(t *testing.T) {
	for _, fixture := range []struct {
		name string
		open func(*testing.T) turnTestStore
	}{
		{name: "memory", open: func(*testing.T) turnTestStore { return NewMemory() }},
		{name: "sql", open: openTurnSQLStore},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := t.Context()
			st := fixture.open(t)
			a, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session a", err)
			b, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session b", err)

			turnA, err := st.BeginTurn(ctx, TurnStart{
				ID: uuid.NewString(), SessionID: a.ID, ProjectID: a.ProjectID,
				Origin: TurnOriginUser, InputJSON: `{}`,
			})
			testutil.FailErr(t, "begin turn a", err)
			turnB, err := st.BeginTurn(ctx, TurnStart{
				ID: uuid.NewString(), SessionID: b.ID, ProjectID: b.ProjectID,
				Origin: TurnOriginUser, InputJSON: `{}`,
			})
			testutil.FailErr(t, "begin turn b", err)
			testutil.FailErr(t, "mark a busy", st.SetSessionStatus(ctx, a.ID, api.SessionStatusBusy))
			testutil.FailErr(t, "mark b busy", st.SetSessionStatus(ctx, b.ID, api.SessionStatusBusy))

			recovered, err := st.RecoverTurnsForSession(ctx, a.ID)
			testutil.FailErr(t, "recover turns for session a", err)
			if len(recovered) != 1 || recovered[0].ID != turnA.Turn.ID {
				t.Fatalf("recovered = %+v, want only turn a (%s)", recovered, turnA.Turn.ID)
			}

			// Turn b's attempt must still be resumable as a live, in-flight
			// attempt — CheckpointTurn only succeeds against a 'running' attempt,
			// so this fails if the scoped call fenced it to interrupted/recovering.
			testutil.FailErr(t, "checkpoint still-running turn b",
				st.CheckpointTurn(ctx, turnB.Turn.ID, turnB.Attempt.ID, TurnPhaseModel, `{}`))

			// The global sweep still finds the other session; recovering rows may also include a.
			globallyRecovered, err := st.RecoverTurns(ctx)
			testutil.FailErr(t, "recover turns global", err)
			foundB := false
			for _, turn := range globallyRecovered {
				if turn.ID == turnB.Turn.ID {
					foundB = true
				}
			}
			if !foundB {
				t.Fatalf("global recovery after scoped call = %+v, want turn b (%s) present", globallyRecovered, turnB.Turn.ID)
			}
		})
	}
}

func TestSettledModelOutputRepairsProjectionParity(t *testing.T) {
	for _, fixture := range []struct {
		name string
		open func(*testing.T) turnTestStore
	}{
		{name: "memory", open: func(*testing.T) turnTestStore { return NewMemory() }},
		{name: "sql", open: openTurnSQLStore},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			st := fixture.open(t)
			sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			execution, err := st.BeginTurn(ctx, TurnStart{
				SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginUser, InputJSON: `{}`,
			})
			testutil.FailErr(t, "begin turn", err)
			outputID := uuid.NewString()
			testutil.FailErr(t, "project live output", st.ProjectLiveModelOutput(ctx, LiveModelOutput{
				ID: outputID, TurnAttemptID: execution.Attempt.ID, SessionID: sess.ID,
				Iteration: 1, MessageID: outputID, Content: "partial",
			}))
			settled, err := st.SettleModelOutput(ctx, ModelOutput{
				ID: outputID, TurnAttemptID: execution.Attempt.ID, SessionID: sess.ID,
				Iteration: 1, MessageID: outputID, Content: "complete", FinishReason: "stop",
			})
			testutil.FailErr(t, "settle output", err)
			pending, err := st.ListPendingModelOutputProjections(ctx)
			testutil.FailErr(t, "list pending projections", err)
			if len(pending) != 1 || pending[0].Output.Content != settled.Content {
				t.Fatalf("pending = %+v", pending)
			}
			testutil.FailErr(t, "append transcript projection", st.AppendMessages(ctx, sess.ID, api.Message{
				ID: outputID, Role: api.MessageRoleAssistant, Content: settled.Content,
				Origin: api.MessageOriginModel, Authority: api.ContentAuthorityNone,
			}))
			testutil.FailErr(t, "ack projection", st.MarkModelOutputProjected(ctx, outputID))
			pending, err = st.ListPendingModelOutputProjections(ctx)
			testutil.FailErr(t, "list repaired projections", err)
			if len(pending) != 0 {
				t.Fatalf("pending after ack = %+v", pending)
			}
			removed, err := st.TruncateMessagesFrom(ctx, sess.ID, outputID)
			testutil.FailErr(t, "truncate output turn", err)
			if removed != 1 {
				t.Fatalf("removed = %d want 1", removed)
			}
			recovered, err := st.RecoverTurns(ctx)
			testutil.FailErr(t, "recover after truncate", err)
			if len(recovered) != 0 {
				t.Fatalf("truncated turn recovered = %+v", recovered)
			}
		})
	}
}

func TestFinalizingTurnCompletesDuringRecoveryParity(t *testing.T) {
	for _, fixture := range []struct {
		name string
		open func(*testing.T) turnTestStore
	}{
		{name: "memory", open: func(*testing.T) turnTestStore { return NewMemory() }},
		{name: "sql", open: openTurnSQLStore},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := t.Context()
			st := fixture.open(t)
			sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			submissionID := uuid.NewString()
			_, _, err = st.PutPromptSubmission(ctx, PromptSubmission{
				ID: submissionID, SessionID: sess.ID, ProjectID: sess.ProjectID,
				InputDigest: "digest", InputJSON: `{}`, Origin: PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID,
			})
			testutil.FailErr(t, "put submission", err)
			_, claimed, err := st.ClaimPromptSubmission(ctx, submissionID)
			testutil.FailErr(t, "claim submission", err)
			if !claimed {
				t.Fatal("submission was not claimed")
			}
			execution, err := st.BeginTurn(ctx, TurnStart{
				SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginUser,
				InputJSON: `{}`, SubmissionIDs: []string{submissionID},
			})
			testutil.FailErr(t, "begin turn", err)
			checkpoint := `{"final_output_id":"output-1","result":{"message_id":"message-1","stream_url":"/stream"}}`
			testutil.FailErr(t, "checkpoint finalizing", st.CheckpointTurn(
				ctx, execution.Turn.ID, execution.Attempt.ID, TurnPhaseFinalizing, checkpoint,
			))
			recovered, err := st.RecoverTurns(ctx)
			testutil.FailErr(t, "recover finalizing turn", err)
			if len(recovered) != 0 {
				t.Fatalf("finalizing turn was requeued = %+v", recovered)
			}
			submission, err := st.GetPromptSubmission(ctx, submissionID)
			testutil.FailErr(t, "get recovered submission", err)
			if submission.Status != PromptSubmissionComplete || submission.ResultJSON == "" {
				t.Fatalf("recovered submission = %+v", submission)
			}
			queued, err := st.RecoverPromptSubmissions(ctx)
			testutil.FailErr(t, "recover prompt submissions", err)
			if len(queued) != 0 {
				t.Fatalf("completed finalizing submission requeued = %v", queued)
			}
		})
	}
}

func TestWorkerRetryStartsNewAttemptOnSameTurn(t *testing.T) {
	ctx := t.Context()
	st := NewMemory()
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	first, err := st.BeginTurn(ctx, TurnStart{
		SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginWorker,
		WorkerJobID: "worker-job-1", InputJSON: `{}`,
	})
	testutil.FailErr(t, "begin worker turn", err)
	_, err = st.FinishTurn(ctx, first.Turn.ID, first.Attempt.ID, TurnStatusFailed, "", "", "transient provider failure")
	testutil.FailErr(t, "fail worker attempt", err)
	second, err := st.BeginTurn(ctx, TurnStart{
		SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginWorker,
		WorkerJobID: "worker-job-1", InputJSON: `{}`,
	})
	testutil.FailErr(t, "retry worker turn", err)
	if second.Turn.ID != first.Turn.ID || second.Attempt.Attempt != 2 {
		t.Fatalf("retry = %+v, first = %+v", second, first)
	}
}

func TestWorkerTurnRecoveryIsScopedByOriginMemory(t *testing.T) {
	ctx := t.Context()
	st := NewMemory()
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	first, err := st.BeginTurn(ctx, TurnStart{
		SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginWorker,
		WorkerJobID: "worker-job-1", InputJSON: `{}`,
	})
	testutil.FailErr(t, "begin worker turn", err)
	_, err = st.FinishTurn(ctx, first.Turn.ID, first.Attempt.ID, TurnStatusFailed, "", "", "transient provider failure")
	testutil.FailErr(t, "fail worker turn", err)
	closeout, err := st.BeginTurn(ctx, TurnStart{
		SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginWorkerCloseout,
		WorkerJobID: "worker-job-1", InputJSON: `{}`,
	})
	testutil.FailErr(t, "begin worker closeout", err)
	if closeout.Turn.ID == first.Turn.ID || closeout.Attempt.Attempt != 1 {
		t.Fatalf("closeout = %+v, worker turn = %+v", closeout, first)
	}
}

func TestWorkerTurnRecoveryIsScopedByOriginSQL(t *testing.T) {
	ctx := t.Context()
	st := openTurnSQLStore(t).(*SQL)
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	_, err = st.db.ExecContext(ctx, `
		INSERT INTO worker_jobs (
			id, project_id, child_session_id, agent_type, status, prompt, brief, created_at
		) VALUES (?, ?, ?, 'repo-researcher', 'running', 'fixture', 'fixture', ?)
	`, "worker-job-1", testdbseed.DefaultProjectID, sess.ID, time.Now().UTC().Format(time.RFC3339Nano))
	testutil.FailErr(t, "insert worker job", err)

	first, err := st.BeginTurn(ctx, TurnStart{
		SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginWorker,
		WorkerJobID: "worker-job-1", InputJSON: `{}`,
	})
	testutil.FailErr(t, "begin worker turn", err)
	_, err = st.FinishTurn(ctx, first.Turn.ID, first.Attempt.ID, TurnStatusFailed, "", "", "transient provider failure")
	testutil.FailErr(t, "fail worker turn", err)
	closeout, err := st.BeginTurn(ctx, TurnStart{
		SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginWorkerCloseout,
		WorkerJobID: "worker-job-1", InputJSON: `{}`,
	})
	testutil.FailErr(t, "begin worker closeout", err)
	if closeout.Turn.ID == first.Turn.ID || closeout.Attempt.Attempt != 1 {
		t.Fatalf("closeout = %+v, worker turn = %+v", closeout, first)
	}
	_, err = st.FinishTurn(ctx, closeout.Turn.ID, closeout.Attempt.ID, TurnStatusComplete, "", `{}`, "")
	testutil.FailErr(t, "complete worker closeout", err)
	retry, err := st.BeginTurn(ctx, TurnStart{
		SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: TurnOriginWorker,
		WorkerJobID: "worker-job-1", InputJSON: `{}`,
	})
	testutil.FailErr(t, "retry worker turn", err)
	if retry.Turn.ID != first.Turn.ID || retry.Attempt.Attempt != 2 {
		t.Fatalf("retry = %+v, first = %+v", retry, first)
	}
}

func openTurnSQLStore(t *testing.T) turnTestStore {
	t.Helper()
	handle := testdbfixture.Open(t, "turns.db")
	testdbseed.InsertProjectRoot(t, handle, testdbseed.DefaultProjectID, t.TempDir())
	return NewSQL(handle)
}
