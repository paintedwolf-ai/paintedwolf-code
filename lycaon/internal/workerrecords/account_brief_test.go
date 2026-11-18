package workerrecords_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerrecords"
)

func TestAccountJobsSetBriefAndOperationLookup(t *testing.T) {
	f := newFixture(t)
	w, _ := f.workerWithRound(t, "fixer", "child-1", "job-1")
	f.insertResult(t, "res-1", "job-1")
	f.settle(t, "job-1", "")

	testutil.FailErr(t, "account jobs", f.store.AccountJobs(t.Context(), []string{"", "  ", "job-1", "job-missing"}))
	dispositions, err := f.store.Dispositions(t.Context(), f.coord, w.WorkerID)
	testutil.FailErr(t, "dispositions", err)
	if len(dispositions) != 1 || dispositions[0].AccountedAt == "" {
		t.Fatalf("dispositions after AccountJobs = %+v", dispositions)
	}

	_, err = f.store.SetBrief(t.Context(), f.coord, op("brief-empty"), w.WorkerID, "   ")
	wantErr(t, "empty brief", err, workerrecords.ErrInvalidProfile)
	updated, err := f.store.SetBrief(t.Context(), f.coord, op("brief-1"), w.WorkerID, "Tighten parser framing")
	testutil.FailErr(t, "set brief", err)
	if updated.Brief != "Tighten parser framing" {
		t.Fatalf("updated brief = %q", updated.Brief)
	}

	_, _, ok, err := f.store.Operation(t.Context(), f.coord, "never-used")
	testutil.FailErr(t, "missing operation", err)
	if ok {
		t.Fatal("unexpected receipt for unused operation")
	}
	kind, raw, ok, err := f.store.Operation(t.Context(), f.coord, "brief-1")
	testutil.FailErr(t, "lookup operation", err)
	if !ok || kind != "set_brief" || len(raw) == 0 {
		t.Fatalf("operation lookup = (%q, %s, %v)", kind, string(raw), ok)
	}
	_, _, _, err = f.store.Operation(t.Context(), f.other, "brief-1")
	wantErr(t, "foreign session operation lookup", err, workerrecords.ErrOperationConflict)
}
