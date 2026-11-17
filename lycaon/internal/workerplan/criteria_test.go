package workerplan_test

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerplan"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

func receipt(command string, verdict evidence.GateVerdict) workercompletion.WorkerInvocationReceipt {
	return workercompletion.WorkerInvocationReceipt{Command: command, Verdict: string(verdict)}
}

// Each typed criterion passes on its own evidence and fails with a reason
// that names what is missing.
func TestEachCriterionIsDecidedByItsOwnEvidence(t *testing.T) {
	env := newPlanEnv(t)
	env.diff.uncommitted = map[string]bool{"pkg/uncommitted.go": true}
	env.diff.sinceBase = map[string]bool{"pkg/committed.go": true}
	env.source.runs = map[string][]workercompletion.WorkerInvocationReceipt{
		"child": {
			receipt("go test ./flaky/...", evidence.GateVerdictFailed),
			receipt("go test ./flaky/...", evidence.GateVerdictPassed),
			receipt("go test ./broken/...", evidence.GateVerdictPassed),
			receipt("go test ./broken/...", evidence.GateVerdictFailed),
		},
	}
	delivered := workerplan.DeliveryInput{
		ChildSessionID: "child",
		ProjectDir:     "/repo",
		JobComplete:    true,
		Summary:        "Implemented package changes.",
		ModifiedPaths:  []string{"pkg/diverged.go"},
		ChangeReport: &api.WorkerChangeReport{
			ChangedPaths: []string{"pkg/reported.go"},
			DiffStat:     []api.WorkerDiffStat{{Path: "pkg/stat.go"}},
		},
	}
	incomplete := delivered
	incomplete.JobComplete, incomplete.Summary = false, "   "
	noSession := delivered
	noSession.ChildSessionID = ""
	noWorkspace := delivered
	noWorkspace.ProjectDir = ""

	for _, tc := range []struct {
		criterion string
		in        workerplan.DeliveryInput
		detail    string
	}{
		{"job:complete", delivered, ""},
		{"job:complete", incomplete, "round_incomplete"},
		{"summary:present", delivered, ""},
		{"summary:present", incomplete, "summary_empty"},
		{"file:modified:pkg/diverged.go", delivered, ""},
		{"file:modified:pkg/reported.go", delivered, ""},
		{"file:modified:pkg/stat.go", delivered, ""},
		{"file:modified:pkg/uncommitted.go", delivered, ""},
		{"file:modified:pkg/committed.go", delivered, ""},
		{"file:modified:pkg/untouched.go", delivered, "file_unmodified"},
		{"file:modified:pkg/uncommitted.go", noWorkspace, "file_unmodified"},
		{"file:modified:", delivered, "target_empty"},
		{"test:pass:go test ./flaky/...", delivered, ""},
		{"test:pass:go test ./broken/...", delivered, "run_failed"},
		{"test:pass:go test ./never/...", delivered, "run_missing"},
		{"test:pass:go test ./flaky/...", noSession, "run_missing"},
		{"reviewed by a person", delivered, ""},
	} {
		tc.in.Worker = workerrecords.Worker{BaseSHA: "abc1234", Criteria: []string{tc.criterion}}
		outcomes, err := env.svc.EvaluateCriteria(t.Context(), tc.in)
		testutil.FailErr(t, tc.criterion, err)
		want := workerrecords.CriterionOutcome{Criterion: tc.criterion, Passed: tc.detail == "", Detail: tc.detail}
		if len(outcomes) != 1 || outcomes[0] != want {
			t.Fatalf("outcomes = %+v, want %+v", outcomes, want)
		}
	}
}

func TestRoundDeliveryIsEvaluatedAgainstItsWorkersCriteria(t *testing.T) {
	env := newPlanEnv(t)
	w := env.worker(t, workerrecords.WorkerSpec{Name: "with-criteria", Criteria: []string{"job:complete", "summary:present"}})
	env.startRound(t, w.ID, "job-1")
	plain := env.worker(t, workerrecords.WorkerSpec{Name: "plain"})
	env.startRound(t, plain.ID, "job-2")

	outcomes, err := env.svc.EvaluateRoundDelivery(t.Context(), env.coord.SessionID, "job-1", workerplan.DeliveryInput{JobComplete: true})
	testutil.FailErr(t, "evaluate", err)
	want := []workerrecords.CriterionOutcome{
		{Criterion: "job:complete", Passed: true},
		{Criterion: "summary:present", Detail: "summary_empty"},
	}
	if len(outcomes) != 2 || outcomes[0] != want[0] || outcomes[1] != want[1] {
		t.Fatalf("outcomes = %+v, want %+v", outcomes, want)
	}

	for _, jobID := range []string{"job-2", "job-of-no-worker"} {
		outcomes, err := env.svc.EvaluateRoundDelivery(t.Context(), env.coord.SessionID, jobID, workerplan.DeliveryInput{JobComplete: true})
		testutil.FailErr(t, "evaluate "+jobID, err)
		if outcomes != nil {
			t.Fatalf("%s: outcomes = %+v, want none", jobID, outcomes)
		}
	}
}

// Evidence that cannot be read is an error, never a failed criterion.
func TestUnreadableEvidenceIsAnErrorNotAFailedCriterion(t *testing.T) {
	env := newPlanEnv(t)
	env.source.err = errors.New("evidence store offline")
	_, err := env.svc.EvaluateCriteria(t.Context(), workerplan.DeliveryInput{
		Worker:         workerrecords.Worker{Criteria: []string{"test:pass:go test ./..."}},
		ChildSessionID: "child",
	})
	if !errors.Is(err, env.source.err) {
		t.Fatalf("error = %v, want the read error", err)
	}
}
