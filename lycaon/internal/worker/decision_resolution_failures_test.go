package worker

import (
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func decisionFailureFixture(t *testing.T) (*db.Store, *SQLDecisionResolver, api.WorkerDecisionRequest) {
	t.Helper()
	database := testdbfixture.Open(t, "decision.db")
	testdbseed.InsertSession(t, database, "parent", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "child", testdbseed.DefaultProjectID)
	_, err := database.ExecContext(t.Context(), `INSERT INTO worker_jobs(id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at)
		VALUES ('job', ?, 'parent', 'child', 'implementer', 'held', 'work', 'work', ?)`, testdbseed.DefaultProjectID, time.Now().UTC().Format(time.RFC3339Nano))
	testutil.FailErr(t, "seed held job", err)
	decisions := sessiondecisions.NewSQL(database)
	testutil.FailErr(t, "store decision", decisions.Put(t.Context(), api.WorkerDecisionRequest{
		ChildSessionID: "child", WorkerID: "job", Question: "Choose", Options: []string{"A", "B"},
	}))
	decision, _, err := decisions.Get(t.Context(), "child")
	testutil.FailErr(t, "read normalized decision", err)
	testutil.FailErr(t, "record delivered outcome", db.New(database).MarkWorkerOutcomeDelivered(t.Context(), db.MarkWorkerOutcomeDeliveredParams{
		WorkerJobID: "job", DeliveredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}))
	messages := sessionstore.NewSQL(database)
	queue := NewSQLQueue(database, 1)
	outbox := eventoutbox.New(database, nil)
	messages.SetEventOutbox(outbox)
	queue.SetEventOutbox(outbox)
	return database, NewSQLDecisionResolver(messages, queue), decision
}

func TestDecisionResolutionPersistenceFailuresPreserveRetry(t *testing.T) {
	for _, operation := range []string{"UPDATE ON worker_jobs", "DELETE ON worker_outcome_deliveries", "INSERT ON event_outbox", "DELETE ON worker_decisions"} {
		t.Run(operation, func(t *testing.T) {
			database, resolver, decision := decisionFailureFixture(t)
			_, err := database.ExecContext(t.Context(), "CREATE TRIGGER reject_resolution BEFORE "+operation+" BEGIN SELECT RAISE(ABORT, 'injected decision failure'); END")
			testutil.FailErr(t, "install failure", err)
			answer := api.Message{Role: api.MessageRoleUser, Content: "A"}
			if err := resolver.Resolve(t.Context(), decision, answer); err == nil {
				t.Fatal("failed persistence accepted the answer")
			}
			var status string
			testutil.FailErr(t, "read held job", database.QueryRowContext(t.Context(), "SELECT status FROM worker_jobs WHERE id='job'").Scan(&status))
			if status != "held" {
				t.Fatalf("failed answer resumed job: %s", status)
			}
			for table, want := range map[string]int{"worker_decisions": 1, "worker_outcome_deliveries": 1, "messages": 0, "event_outbox": 0} {
				var count int
				testutil.FailErr(t, "read rolled-back "+table, database.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count))
				if count != want {
					t.Fatalf("%s has %d rows, want %d", table, count, want)
				}
			}
			_, err = database.ExecContext(t.Context(), "DROP TRIGGER reject_resolution")
			testutil.FailErr(t, "remove failure", err)
			testutil.FailErr(t, "retry the same answer", resolver.Resolve(t.Context(), decision, answer))
			if _, pending, err := sessiondecisions.NewSQL(database).Get(t.Context(), "child"); err != nil || pending {
				t.Fatalf("retry did not settle decision: pending=%v error=%v", pending, err)
			}
		})
	}
}

func TestDecisionResolutionRejectsMissingOrForeignDecision(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(map[bool]string{true: "missing", false: "another job"}[missing], func(t *testing.T) {
			database, resolver, decision := decisionFailureFixture(t)
			want := errDecisionJobMismatch
			if missing {
				_, err := database.ExecContext(t.Context(), "DELETE FROM worker_decisions")
				testutil.FailErr(t, "remove decision", err)
				want = errDecisionMissing
			} else {
				decision.WorkerID = "another-job"
			}
			err := resolver.Resolve(t.Context(), decision, api.Message{Role: api.MessageRoleUser, Content: "A"})
			if !errors.Is(err, want) {
				t.Fatalf("refusal = %v, want %v", err, want)
			}
			var status string
			testutil.FailErr(t, "read refused job", database.QueryRowContext(t.Context(), "SELECT status FROM worker_jobs WHERE id='job'").Scan(&status))
			var messages int
			testutil.FailErr(t, "read refused transcript", database.QueryRowContext(t.Context(), "SELECT count(*) FROM messages").Scan(&messages))
			if status != "held" || messages != 0 {
				t.Fatalf("refused answer changed state: status=%s messages=%d", status, messages)
			}
		})
	}
}
