package progress_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	api "github.com/lycaon/lycaon/pkg/api"
)

type interruptedTransaction struct {
	progress.TranscriptTransactions
	after func(*sql.Tx) error
}

func (s interruptedTransaction) AppendMessagesWith(ctx context.Context, id string, mutate func(*sql.Tx) ([]api.Message, error)) error {
	return s.TranscriptTransactions.AppendMessagesWith(ctx, id, func(tx *sql.Tx) ([]api.Message, error) {
		rows, err := mutate(tx)
		if err != nil {
			return nil, err
		}
		if err = s.after(tx); err != nil {
			return nil, err
		}
		return rows, nil
	})
}

func TestPlanAndTranscriptRollbackTogether(t *testing.T) {
	for _, mode := range []string{"plan insertion", "message insertion", "outbox insertion", "before commit", "commit constraint", "cancellation"} {
		t.Run(mode, func(t *testing.T) {
			f := newProgressFixture(t)
			before, err := f.plans.Snapshot(t.Context(), "session")
			testutil.FailErr(t, "read original plan", err)
			table := map[string]string{"plan insertion": "session_progress", "message insertion": "messages", "outbox insertion": "event_outbox"}[mode]
			if table != "" {
				_, err = f.database.ExecContext(t.Context(), "CREATE TRIGGER reject_progress BEFORE INSERT ON "+table+" BEGIN SELECT RAISE(ABORT, 'injected persistence failure'); END")
				testutil.FailErr(t, "install failure", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			writer := progress.TranscriptTransactions(f.sessions)
			if mode == "before commit" || mode == "cancellation" || mode == "commit constraint" {
				if mode == "commit constraint" {
					_, err = f.database.ExecContext(t.Context(), "CREATE TABLE deferred_failure (session_id TEXT REFERENCES sessions(id) DEFERRABLE INITIALLY DEFERRED)")
					testutil.FailErr(t, "create deferred constraint", err)
				}
				writer = interruptedTransaction{f.sessions, func(tx *sql.Tx) error {
					switch mode {
					case "cancellation":
						cancel()
						return ctx.Err()
					case "commit constraint":
						_, err := tx.ExecContext(ctx, "INSERT INTO deferred_failure VALUES ('missing')")
						return err
					default:
						return errors.New("injected commit interruption")
					}
				}}
			}
			plans := progress.NewSQLStore(f.database, writer)
			notified := 0
			detach := progress.RegisterWriteObserver(func(_ context.Context, event progress.WriteEvent) {
				if event.SessionID == "session" {
					notified++
				}
			})
			defer detach()
			_, err = plans.Update(ctx, "session", func(progress.Snapshot) (progress.Document, error) {
				return progress.Document{Content: "- [ ] Keep"}, nil
			})
			if err == nil {
				t.Fatal("injected failure accepted")
			}
			after, err := f.plans.Snapshot(t.Context(), "session")
			testutil.FailErr(t, "read rolled-back plan", err)
			if !before.Equal(after) || len(f.messages(t)) != 0 || notified != 0 {
				t.Fatalf("partial mutation: before=%+v after=%+v notified=%d", before, after, notified)
			}
			var count int
			testutil.FailErr(t, "read outbox", f.database.QueryRowContext(t.Context(), "SELECT count(*) FROM event_outbox").Scan(&count))
			if count != 0 {
				t.Fatalf("rolled-back outbox rows=%d", count)
			}
			if table != "" {
				_, err = f.database.ExecContext(t.Context(), "DROP TRIGGER reject_progress")
				testutil.FailErr(t, "remove injected failure", err)
			}
			if mode == "commit constraint" {
				_, err = f.database.ExecContext(t.Context(), "DROP TABLE deferred_failure")
				testutil.FailErr(t, "remove deferred fixture", err)
			}
			applyPlan(t, f.plans, "- [ ] Keep")
			rows := f.messages(t)
			if len(rows) != 1 || rows[0].Ord != 1 || !rows[0].ProgressUpdate.Initial {
				t.Fatalf("retry skipped initial transition: %+v", rows)
			}
		})
	}
}

func TestPlanRunIdentityIsValidatedBeforeMutation(t *testing.T) {
	for _, runID := range []string{"missing", "foreign"} {
		t.Run(runID, func(t *testing.T) {
			f := newProgressFixture(t)
			if runID == "foreign" {
				testdbseed.InsertWorkflowRun(t, f.database, "foreign", "other", testdbseed.DefaultProjectID)
			}
			before, err := f.plans.Snapshot(t.Context(), "session")
			testutil.FailErr(t, "read before invalid binding", err)
			_, err = f.plans.Update(t.Context(), "session", func(progress.Snapshot) (progress.Document, error) {
				return progress.Document{RunID: runID, Content: "- [ ] Invalid"}, nil
			})
			if err == nil {
				t.Fatal("invalid run accepted")
			}
			after, err := f.plans.Snapshot(t.Context(), "session")
			testutil.FailErr(t, "read after invalid binding", err)
			if !before.Equal(after) || len(f.messages(t)) != 0 {
				t.Fatal("invalid binding mutated history")
			}
		})
	}
}
