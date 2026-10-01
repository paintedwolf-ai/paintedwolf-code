package sourcebranch_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerKindWithoutAJobHasNoBranch(t *testing.T) {
	t.Parallel()
	if _, err := sourcebranch.ForWorker("  "); err == nil {
		t.Fatal("a worker branch with no job id was accepted")
	}
	if _, err := sourcebranch.FromKindAndJob(api.SourceWorkspaceKindWorker, ""); err == nil {
		t.Fatal("worker kind with no job id resolved to a branch")
	}
	if _, err := sourcebranch.FromKindAndJob("chimera", "job-1"); err == nil {
		t.Fatal("an unknown workspace kind resolved to a branch")
	}
}

func TestBranchKindAndIdentity(t *testing.T) {
	t.Parallel()
	trunk, err := sourcebranch.FromKindAndJob(api.SourceWorkspaceKindProject, "")
	testutil.FailErr(t, "resolve the project branch", err)
	if trunk != sourcebranch.Trunk || trunk.IsWorker() {
		t.Fatalf("project kind resolved to %q", trunk)
	}
	if got := trunk.Kind(); got != api.SourceWorkspaceKindProject {
		t.Fatalf("trunk reports kind %q", got)
	}

	worker, err := sourcebranch.ForWorker("  job-9  ")
	testutil.FailErr(t, "resolve the worker branch", err)
	if worker.String() != "job-9" || !worker.IsWorker() {
		t.Fatalf("worker branch = %q", worker)
	}
	if got := worker.Kind(); got != api.SourceWorkspaceKindWorker {
		t.Fatalf("worker branch reports kind %q", got)
	}
}
