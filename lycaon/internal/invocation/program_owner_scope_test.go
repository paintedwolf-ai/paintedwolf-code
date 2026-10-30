package invocation_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestReviewedOwnerStartRejectsForeignCaptureBeforeStorage(t *testing.T) {
	for _, progress := range []bool{false, true} {
		for _, mismatch := range []string{"program", "session", "project", "recorder", "projector", "state", "outputs"} {
			t.Run(mismatch+map[bool]string{true: "/progress", false: "/native"}[progress], func(t *testing.T) {
				f := newProgramReceiptFixture(t)
				bound := f.child(t)
				counted := &countedOwnerTransactions{Handle: f.database}
				f.recorder = invocation.NewSQLRecorder(counted)
				capture := f.capture(t)
				review, release, err := toolcontract.NewReviewedOperationInputs(t.Context(), capture.State, map[string]any{"value": "reviewed"})
				testutil.FailErr(t, "freeze reviewed input", err)
				defer release()
				recorder := f.recorder
				switch mismatch {
				case "program":
					capture.ProgramID = "foreign-program"
				case "session":
					capture.Scope.SessionID = "foreign-session"
				case "project":
					capture.Scope.ProjectID = "foreign-project"
				case "recorder":
					recorder = invocation.NewSQLRecorder(counted)
				case "projector":
					capture.Projector = nil
				case "state":
					capture.State = nil
				case "outputs":
					capture.Outputs = nil
				}
				observer := invocation.ProgramAdmissionRecorder{Recorder: recorder, Inputs: capture}
				before := counted.starts
				if progress {
					id := toolcontract.ProgramProgressIdentity{ProgramID: f.program, ExecutionID: uuid.NewString(), ActorID: "fixture-person", SessionID: f.session, RootSessionID: f.session, Scope: toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope}}
					err = observer.BeforeProgressOwner(t.Context(), id, "actual-model-call", review)
				} else {
					err = observer.BeforeNativeOwner(t.Context(), bound, "actual-model-call", review)
				}
				if err == nil || observer.OwnerStartRecorded || counted.starts != before {
					t.Fatalf("foreign capture reached persistence: error=%v started=%t transactions=%d", err, observer.OwnerStartRecorded, counted.starts-before)
				}
				var inputs, started int
				testutil.FailErr(t, "inspect rejected input persistence", f.database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_outputs WHERE program_id=?`, f.program).Scan(&inputs))
				testutil.FailErr(t, "inspect rejected owner persistence", f.database.QueryRowContext(t.Context(), `SELECT owner_started FROM invocation_program_executions WHERE id=?`, bound.Invocation().Identity.ExecutionID).Scan(&started))
				if inputs != 0 || started != 0 {
					t.Fatalf("foreign capture left inputs=%d started=%d", inputs, started)
				}
			})
		}
	}
}
