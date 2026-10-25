package projectremoval

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestRecoverObservesProjectsAndLeavesActiveOperationsAlone(t *testing.T) {
	o, reg, retained := fixture(t)
	deleted := uuid.NewString()
	var operations []string
	for _, id := range []string{retained, deleted, retained} {
		result := wire.ProjectRemovalResult{OperationID: uuid.NewString(), ProjectID: id, ProjectState: "unknown", Extensions: []string{"keep/extension"}}
		testutil.FailErr(t, "begin interrupted receipt", o.Store.begin(t.Context(), record{ProjectID: id, Request: "{}", Result: result}))
		operations = append(operations, result.OperationID)
		if len(operations) == 3 {
			o.track(result)
		}
	}
	testutil.FailErr(t, "recover interrupted removals", o.Recover(t.Context()))
	for n, operation := range operations {
		row, found, err := o.Store.read(t.Context(), operation)
		testutil.FailErr(t, "read recovered receipt", err)
		if !found {
			t.Fatal("receipt disappeared")
		}
		if n == 2 {
			if row.Settled {
				t.Fatal("active operation was interrupted")
			}
			continue
		}
		want := "retained"
		if n == 1 {
			want = "deleted"
		}
		if !row.Settled || row.Result.ProjectState != want || row.Result.FailureCode != "removal_interrupted" {
			t.Fatalf("recovered receipt = %+v, want settled %s", row, want)
		}
	}
	if reg.rows[retained] == nil {
		t.Fatal("recovery replayed deletion")
	}
	testutil.FailErr(t, "repeat recovery", o.Recover(t.Context()))
}

func TestRecoverKeepsReceiptPendingWhenProjectReadFails(t *testing.T) {
	o, reg, id := fixture(t)
	result := wire.ProjectRemovalResult{OperationID: uuid.NewString(), ProjectID: id, ProjectState: "unknown"}
	testutil.FailErr(t, "begin receipt", o.Store.begin(t.Context(), record{ProjectID: id, Request: "{}", Result: result}))
	reg.getError = errors.New("project read unavailable")
	if err := o.Recover(t.Context()); !errors.Is(err, reg.getError) {
		t.Fatalf("recover error = %v", err)
	}
	row, _, err := o.Store.read(t.Context(), result.OperationID)
	testutil.FailErr(t, "read pending receipt", err)
	if row.Settled {
		t.Fatal("failed observation settled receipt")
	}
	reg.getError = nil
	testutil.FailErr(t, "retry recovery", o.Recover(t.Context()))
}
