package invocation_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestReviewedOwnerStartSurvivesCancellationAndColdRecovery(t *testing.T) {
	for _, progress := range []bool{false, true} {
		for _, size := range []int{16, 17 << 10} {
			t.Run(fmt.Sprintf("progress=%t/bytes=%d", progress, size), func(t *testing.T) {
				f := newProgramReceiptFixture(t)
				bound := f.child(t)
				routed := &countedOwnerTransactions{Handle: f.database}
				f.database = routed
				f.recorder = invocation.NewSQLRecorder(routed)
				t.Cleanup(func() { testutil.FailErr(t, "close reopened store", routed.Close()) })
				root := t.TempDir()
				capture := f.captureAt(t, root)
				review, release, err := toolcontract.NewReviewedOperationInputs(t.Context(), capture.State, map[string]any{"value": strings.Repeat("x", size)})
				testutil.FailErr(t, "freeze reviewed input", err)
				defer release()
				observer := invocation.ProgramAdmissionRecorder{Recorder: f.recorder, Inputs: capture}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				identity := toolcontract.ProgramProgressIdentity{ProgramID: f.program, ExecutionID: uuid.NewString(), ActorID: "fixture-person", SessionID: f.session, RootSessionID: f.session, Scope: toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope}}
				var reference string
				if progress {
					testutil.FailErr(t, "commit progress start", observer.BeforeProgressOwner(ctx, identity, "actual-model-call", review))
					testutil.FailErr(t, "read progress input binding", f.database.QueryRowContext(t.Context(), `SELECT arguments_ref FROM invocation_program_progress WHERE execution_id=?`, identity.ExecutionID).Scan(&reference))
				} else {
					testutil.FailErr(t, "commit native start", observer.BeforeNativeOwner(ctx, bound, "actual-model-call", review))
					testutil.FailErr(t, "read native input binding", f.database.QueryRowContext(t.Context(), `SELECT reviewed_arguments_ref FROM invocation_program_executions WHERE id=?`, bound.Invocation().Identity.ExecutionID).Scan(&reference))
				}
				cancel()
				var seq int
				var name, path string
				testutil.FailErr(t, "locate fixture store", f.database.QueryRowContext(t.Context(), `PRAGMA database_list`).Scan(&seq, &name, &path))
				testutil.FailErr(t, "close original store", routed.Close())
				reopened, err := db.OpenWithOptions(context.Background(), path, db.UpgradeHooks{}, db.StoreOptions{FastTestSync: true})
				testutil.FailErr(t, "reopen durable store", err)
				routed.Handle = reopened
				recorder := invocation.NewSQLRecorder(reopened)
				count, err := recorder.InterruptRunningSession(t.Context(), f.session)
				testutil.FailErr(t, "reconcile interrupted owner", err)
				if count != 1 {
					t.Fatalf("interrupted parent count=%d", count)
				}
				quota := &inputCaptureQuota{}
				recovered, err := invocation.NewProgramOutputRetention(t.Context(), recorder, blobstore.Store{Root: root}, f.session, quota)
				testutil.FailErr(t, "admit recovered inputs", err)
				testutil.FailErr(t, "restore durable input claims", recovered.Restore(t.Context()))
				page, err := recorder.ReadProgramPage(t.Context(), f.session, f.program, "", 10)
				testutil.FailErr(t, "inspect interrupted owner", err)
				if page.Program.State != "interrupted" {
					t.Fatalf("program state=%s", page.Program.State)
				}
				if progress {
					if page.Progress == nil || page.Progress.State != "unknown" || page.Progress.OwnerInvoked {
						t.Fatalf("progress recovery=%+v", page.Progress)
					}
				} else if len(page.Executions) != 1 || page.Executions[0].State != "interrupted" || page.Executions[0].EffectCertainty != "unknown" || page.Executions[0].OwnerInvoked {
					t.Fatalf("native recovery=%+v", page.Executions)
				}
				reader, err := recovered.Open(t.Context(), f.session, f.program, reference)
				testutil.FailErr(t, "open recovered reviewed input", err)
				body, err := io.ReadAll(reader.Reader)
				testutil.FailErr(t, "read recovered bytes", err)
				testutil.FailErr(t, "close recovered reader", reader.Reader.Close())
				if string(body) != review.CanonicalArguments() {
					t.Fatal("recovery changed reviewed input bytes")
				}
				receipts, err := recorder.ListSession(t.Context(), f.session)
				testutil.FailErr(t, "read interrupted model call", err)
				if len(receipts) != 1 || receipts[0].Failure == nil || receipts[0].Failure.Retryable {
					t.Fatal("interrupted program became replayable")
				}
				_, err = f.database.ExecContext(t.Context(), `DELETE FROM invocation_programs WHERE id=?`, f.program)
				testutil.FailErr(t, "release recovered references", err)
				testutil.FailErr(t, "close recovered input lifetime", recovered.Close(t.Context()))
				if quota.state != 0 || quota.disk != 0 || quota.memory != 0 {
					t.Fatalf("recovery leaked resources: %+v", quota)
				}
			})
		}
	}
}
