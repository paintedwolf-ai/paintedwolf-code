package invocation_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"modernc.org/sqlite"
)

type lostCommitConnector struct{ path string }

func (c lostCommitConnector) Driver() driver.Driver { return &sqlite.Driver{} }
func (c lostCommitConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.path + "?_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	return lostCommitConnection{conn}, nil
}

type lostCommitConnection struct{ driver.Conn }

func (c lostCommitConnection) Begin() (driver.Tx, error) {
	tx, err := c.Conn.Begin()
	if err != nil {
		return nil, err
	}
	return lostCommitTransaction{tx}, nil
}

type lostCommitTransaction struct{ driver.Tx }

func (tx lostCommitTransaction) Commit() error {
	if err := tx.Tx.Commit(); err != nil {
		return err
	}
	return errors.New("fixture lost commit acknowledgement")
}

type lostCommitHandle struct {
	db.Handle
	writer *sql.DB
}

func (h lostCommitHandle) BeginTx(ctx context.Context, options *sql.TxOptions) (*sql.Tx, error) {
	return h.writer.BeginTx(ctx, options)
}

func (h lostCommitHandle) BeginReadTx(ctx context.Context) (*sql.Tx, error) {
	return h.Handle.(db.ReadHandle).BeginReadTx(ctx)
}

func TestReviewedOwnerStartRetainsCommittedBytesAfterLostAcknowledgement(t *testing.T) {
	for _, progress := range []bool{false, true} {
		for _, size := range []int{16, 17 << 10} {
			t.Run(fmt.Sprintf("progress=%t/bytes=%d", progress, size), func(t *testing.T) {
				f := newProgramReceiptFixture(t)
				bound := f.child(t)
				var seq int
				var name, path string
				testutil.FailErr(t, "locate fixture store", f.database.QueryRowContext(t.Context(), `PRAGMA database_list`).Scan(&seq, &name, &path))
				writer := sql.OpenDB(lostCommitConnector{path: path})
				t.Cleanup(func() { testutil.FailErr(t, "close fault writer", writer.Close()) })
				fault := lostCommitHandle{Handle: f.database, writer: writer}
				f.recorder = invocation.NewSQLRecorder(fault)
				capture := f.capture(t)
				review, release, err := toolcontract.NewReviewedOperationInputs(t.Context(), capture.State, map[string]any{"value": strings.Repeat("x", size)})
				testutil.FailErr(t, "freeze reviewed input", err)
				defer release()
				observer := invocation.ProgramAdmissionRecorder{Recorder: f.recorder, Inputs: capture}
				identity := toolcontract.ProgramProgressIdentity{ProgramID: f.program, ExecutionID: uuid.NewString(), ActorID: "fixture-person", SessionID: f.session, RootSessionID: f.session, Scope: toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope}}
				execution := bound.Invocation().Identity.ExecutionID
				if progress {
					execution = identity.ExecutionID
					err = observer.BeforeProgressOwner(t.Context(), identity, "actual-model-call", review)
				} else {
					err = observer.BeforeNativeOwner(t.Context(), bound, "actual-model-call", review)
				}
				if err == nil || observer.OwnerStartRecorded {
					t.Fatal("lost acknowledgement authorized owner dispatch")
				}
				var reference, state string
				if progress {
					testutil.FailErr(t, "read committed progress", f.database.QueryRowContext(t.Context(), `SELECT state,arguments_ref FROM invocation_program_progress WHERE execution_id=?`, execution).Scan(&state, &reference))
				} else {
					testutil.FailErr(t, "read committed native marker", f.database.QueryRowContext(t.Context(), `SELECT state,reviewed_arguments_ref FROM invocation_program_executions WHERE id=?`, execution).Scan(&state, &reference))
				}
				if state != "running" || reference == "" {
					t.Fatalf("fixture did not commit owner marker: state=%s ref=%q", state, reference)
				}
				testutil.FailErr(t, "preserve attached claims during collection", capture.Outputs.Collect(t.Context()))
				testutil.FailErr(t, "reconcile durable ambiguous attachment", capture.Outputs.Restore(t.Context()))
				reader, err := capture.Outputs.Open(t.Context(), f.session, f.program, reference)
				testutil.FailErr(t, "open committed input after failed acknowledgement", err)
				body, err := io.ReadAll(reader.Reader)
				testutil.FailErr(t, "read committed input", err)
				testutil.FailErr(t, "close committed input", reader.Reader.Close())
				if string(body) != review.CanonicalArguments() {
					t.Fatal("ambiguous attachment lost its reviewed bytes")
				}
				recorder := invocation.NewSQLRecorder(f.database)
				count, err := recorder.InterruptRunningSession(t.Context(), f.session)
				testutil.FailErr(t, "reconcile interrupted owner", err)
				if count != 1 {
					t.Fatalf("interrupted model parents=%d", count)
				}
				page, err := recorder.ReadProgramPage(t.Context(), f.session, f.program, "", 10)
				testutil.FailErr(t, "read recovery without replay", err)
				if page.Program.State != "interrupted" {
					t.Fatalf("recovery state=%s", page.Program.State)
				}
			})
		}
	}
}
