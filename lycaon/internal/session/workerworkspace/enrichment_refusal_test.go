package workerworkspace

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/call"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type enrichmentJobs struct {
	task     *api.WorkerTask
	claimErr error
	claims   int
}

func (j *enrichmentJobs) Get(id string) (*api.WorkerTask, bool) {
	return j.task, j.task != nil && id == j.task.ID
}
func (j *enrichmentJobs) ClaimWorkerBranch(context.Context, string) (*api.WorkerTask, error) {
	j.claims++
	return nil, j.claimErr
}

func TestReadWorkerEnrichmentKeepsProjectSourcesAndContextIdentity(t *testing.T) {
	service := New(nil, nil, nil)
	jobs := &enrichmentJobs{task: &api.WorkerTask{ID: "read-job"}}
	service.SetTasks(jobs)
	input := tools.ToolContext{Identity: tools.InvocationIdentity{RootSessionID: "retained-root"}}
	child := &api.Session{ID: "child", ParentSessionID: " parent "}
	out, err := service.Enrich(workercontext.WithJob(t.Context(), "read-job"), child, input)
	testutil.FailErr(t, "enrich read worker", err)
	if out.Identity.WorkerJobID != "read-job" || out.Identity.ParentSessionID != "parent" || out.Identity.HandoffSessionID != "parent" || out.Identity.HandoffAgentID != "read-job" || out.Identity.RootSessionID != "retained-root" || out.Source.SourceWorkspaceKind != api.SourceWorkspaceKindProject || out.Source.WorkerCoord != service || jobs.claims != 0 {
		t.Fatalf("enriched=%+v claims=%d", out, jobs.claims)
	}
	input.Identity.RootSessionID = ""
	out, err = service.Enrich(workercontext.WithJob(t.Context(), "read-job"), child, input)
	testutil.FailErr(t, "enrich without root repository", err)
	if out.Identity.RootSessionID != "parent" {
		t.Fatalf("fallback root=%q", out.Identity.RootSessionID)
	}
	_, err = service.Enrich(t.Context(), child, input)
	if err == nil {
		t.Fatal("child worker accepted missing job identity")
	}
}

func TestWorkerBranchClaimFailurePreservesInvocation(t *testing.T) {
	cause := errors.New("branch allocation failed")
	jobs := &enrichmentJobs{task: &api.WorkerTask{ID: "write-job", Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite}}, claimErr: cause}
	service := New(nil, nil, nil)
	service.SetTasks(jobs)
	input := tools.ToolContext{Identity: tools.InvocationIdentity{WorkerJobID: "write-job", RootSessionID: "root"}}
	child := &api.Session{ID: "child", ParentSessionID: "parent"}
	out, err := service.Enrich(t.Context(), child, input)
	if !errors.Is(err, cause) || !reflect.DeepEqual(out, input) || jobs.claims != 1 {
		t.Fatalf("enrichment=%+v error=%v claims=%d", out, err, jobs.claims)
	}
}

type writeReservations struct {
	Calls
	paths          []string
	session, agent string
}

func (c *writeReservations) Reserve(_ context.Context, session string, paths []string, agent string) (*call.ReservationResult, error) {
	c.session = session
	c.agent = agent
	c.paths = append(c.paths, paths...)
	return &call.ReservationResult{}, nil
}
func TestWorkerWriteReservesNormalizedTouchAndRefusesReadScopedMutation(t *testing.T) {
	service := New(nil, nil, nil)
	calls := &writeReservations{}
	service.calls = calls
	jobs := &enrichmentJobs{task: &api.WorkerTask{ID: "job", Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite}}}
	service.SetTasks(jobs)
	ctx := tools.ToolContext{Identity: tools.InvocationIdentity{WorkerJobID: "job", HandoffSessionID: "parent", HandoffAgentID: "job"}}
	testutil.FailErr(t, "reserve write", service.BeforeWorkerWrite(t.Context(), ctx, "folder/../item.txt"))
	if calls.session != "parent" || calls.agent != "job" || !reflect.DeepEqual(calls.paths, []string{"item.txt"}) || !reflect.DeepEqual(service.Touches.Paths("job"), []string{"item.txt"}) {
		t.Fatalf("reservation=%+v touches=%v", calls, service.Touches.Paths("job"))
	}
	jobs.task.Scope = &api.TaskScope{Mode: api.TaskScopeModeRead}
	err := service.BeforeWorkerWrite(t.Context(), ctx, "other.txt")
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != TaskScopeReadMutationDeniedCode || len(calls.paths) != 1 || len(service.Touches.Paths("job")) != 1 {
		t.Fatalf("read worker changed touch/reservation err=%v calls=%+v", err, calls)
	}
}
