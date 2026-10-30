package invocation_test

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type countedOwnerTransactions struct {
	db.Handle
	starts int
	cancel context.CancelFunc
}

func (h *countedOwnerTransactions) BeginTx(ctx context.Context, options *sql.TxOptions) (*sql.Tx, error) {
	h.starts++
	tx, err := h.Handle.BeginTx(ctx, options)
	if h.cancel != nil {
		h.cancel()
	}
	return tx, err
}

func TestReviewedOwnerStartCommitsOneAtomicBoundary(t *testing.T) {
	for _, progress := range []bool{false, true} {
		for _, size := range []int{16, 17 << 10} {
			for _, outcome := range []string{"commit", "owner-refused", "input-refused", "wrong-model-call", "cancelled"} {
				t.Run(fmt.Sprintf("progress=%t/bytes=%d/%s", progress, size, outcome), func(t *testing.T) {
					f := newProgramReceiptFixture(t)
					bound := f.child(t)
					counted := &countedOwnerTransactions{Handle: f.database}
					f.recorder = invocation.NewSQLRecorder(counted)
					capture := f.capture(t)
					observer := invocation.ProgramAdmissionRecorder{Recorder: f.recorder, Inputs: capture}
					execution := bound.Invocation().Identity.ExecutionID
					identity := toolcontract.ProgramProgressIdentity{ProgramID: f.program, ExecutionID: uuid.NewString(), ActorID: "fixture-person", SessionID: f.session, RootSessionID: f.session, Scope: toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope}}
					if progress {
						execution = identity.ExecutionID
					}
					review, release, err := toolcontract.NewReviewedOperationInputs(t.Context(), capture.State, map[string]any{"value": strings.Repeat("x", size)})
					testutil.FailErr(t, "freeze reviewed input", err)
					defer release()
					if outcome == "owner-refused" {
						query := `CREATE TRIGGER fixture_owner_fault BEFORE UPDATE OF owner_started ON invocation_program_executions BEGIN SELECT RAISE(ABORT,'fixture fault'); END`
						if progress {
							query = `CREATE TRIGGER fixture_owner_fault BEFORE INSERT ON invocation_program_progress BEGIN SELECT RAISE(ABORT,'fixture fault'); END`
						}
						_, err = f.database.ExecContext(t.Context(), query)
						testutil.FailErr(t, "install owner marker fault", err)
					}
					if outcome == "input-refused" {
						_, err = f.database.ExecContext(t.Context(), `CREATE TRIGGER fixture_input_fault BEFORE INSERT ON invocation_program_outputs BEGIN SELECT RAISE(ABORT,'fixture fault'); END`)
						testutil.FailErr(t, "install input attachment fault", err)
					}
					call := "actual-model-call"
					if outcome == "wrong-model-call" {
						call = "foreign-model-call"
					}
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					if outcome == "cancelled" {
						counted.cancel = cancel
					}
					var events, bytes int64
					testutil.FailErr(t, "read original trace", f.database.QueryRowContext(t.Context(), `SELECT trace_events,trace_bytes FROM invocation_programs WHERE id=?`, f.program).Scan(&events, &bytes))
					before := counted.starts
					start := func() error {
						if progress {
							return observer.BeforeProgressOwner(ctx, identity, call, review)
						}
						return observer.BeforeNativeOwner(ctx, bound, call, review)
					}
					err = start()
					if counted.starts-before != 1 {
						t.Fatalf("owner boundary opened %d transactions, want one", counted.starts-before)
					}
					if (err == nil) != (outcome == "commit") {
						t.Fatalf("start outcome=%s error=%v", outcome, err)
					}
					var bodyCount int
					testutil.FailErr(t, "inspect attached reviewed inputs", f.database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_outputs WHERE program_id=? AND initiating_claim_id=?`, f.program, execution).Scan(&bodyCount))
					var state, reference, digest string
					var started bool
					if progress {
						var count int
						testutil.FailErr(t, "inspect progress start", f.database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_progress WHERE execution_id=?`, execution).Scan(&count))
						started = count == 1
						if started {
							testutil.FailErr(t, "read progress input binding", f.database.QueryRowContext(t.Context(), `SELECT state,arguments_ref,arguments_digest FROM invocation_program_progress WHERE execution_id=?`, execution).Scan(&state, &reference, &digest))
						}
					} else {
						testutil.FailErr(t, "read native start", f.database.QueryRowContext(t.Context(), `SELECT state,owner_started,reviewed_arguments_ref,reviewed_arguments_digest FROM invocation_program_executions WHERE id=?`, execution).Scan(&state, &started, &reference, &digest))
					}
					if outcome == "commit" {
						if !started || state != "running" || bodyCount != 1 || reference == "" || digest != review.ArgumentsDigest() {
							t.Fatalf("incomplete durable boundary: started=%t state=%s inputs=%d ref=%q", started, state, bodyCount, reference)
						}
						reader, err := capture.Outputs.Open(t.Context(), f.session, f.program, reference)
						testutil.FailErr(t, "open committed reviewed inputs", err)
						body, err := io.ReadAll(reader.Reader)
						testutil.FailErr(t, "read reviewed inputs", err)
						testutil.FailErr(t, "close reviewed inputs", reader.Reader.Close())
						if string(body) != review.CanonicalArguments() {
							t.Fatal("committed reviewed bytes differ")
						}
						if err := start(); err == nil {
							t.Fatal("duplicate owner start succeeded")
						}
						var afterCount int
						testutil.FailErr(t, "inspect duplicate rollback", f.database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_outputs WHERE program_id=? AND initiating_claim_id=?`, f.program, execution).Scan(&afterCount))
						if afterCount != bodyCount {
							t.Fatal("duplicate start retained an orphan inspection body")
						}
					} else {
						var afterEvents, afterBytes int64
						testutil.FailErr(t, "read failed trace", f.database.QueryRowContext(t.Context(), `SELECT trace_events,trace_bytes FROM invocation_programs WHERE id=?`, f.program).Scan(&afterEvents, &afterBytes))
						if started || bodyCount != 0 || reference != "" || afterEvents != events || afterBytes != bytes {
							t.Fatalf("partial owner boundary: started=%t inputs=%d ref=%q trace=%d/%d", started, bodyCount, reference, afterEvents, afterBytes)
						}
					}
					_, err = f.database.ExecContext(t.Context(), `DELETE FROM invocation_programs WHERE id=?`, f.program)
					testutil.FailErr(t, "release fixture references", err)
					testutil.FailErr(t, "collect retained owner input", capture.Outputs.Close(t.Context()))
				})
			}
		}
	}
}
