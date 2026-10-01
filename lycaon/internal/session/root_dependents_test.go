package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubProjectWorkerLister struct {
	noopWorkerBranchClaim
	tasks []api.WorkerTask
}

func (*stubProjectWorkerLister) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) {
	return nil, nil
}

func (s *stubProjectWorkerLister) ListBySession(_ context.Context, projectID, sessionID string, status ...api.WorkerStatus) ([]api.WorkerTask, error) {
	all, err := s.List(context.Background(), projectID, status...)
	if err != nil {
		return nil, err
	}
	var out []api.WorkerTask
	for _, task := range all {
		if task.ParentSessionID == sessionID {
			out = append(out, task)
		}
	}
	return out, nil
}

func (s *stubProjectWorkerLister) List(_ context.Context, projectID string, status ...api.WorkerStatus) ([]api.WorkerTask, error) {
	want := map[api.WorkerStatus]struct{}{}
	for _, st := range status {
		want[st] = struct{}{}
	}
	var out []api.WorkerTask
	for _, task := range s.tasks {
		if projectID != "" && task.ProjectID != projectID {
			continue
		}
		if len(want) > 0 {
			if _, ok := want[task.Status]; !ok {
				continue
			}
		}
		out = append(out, task)
	}
	return out, nil
}

func (s *stubProjectWorkerLister) Get(jobID string) (*api.WorkerTask, bool) {
	for i := range s.tasks {
		if s.tasks[i].ID == jobID {
			return &s.tasks[i], true
		}
	}
	return nil, false
}

func TestRootDependentsInFlightWorkerAndBusySession(t *testing.T) {
	ctx := context.Background()
	reg := project.NewMemoryRegistry()
	dir := t.TempDir()
	p, err := reg.Create(ctx, project.CreateParams{
		Roots: []project.AttachRootParams{{Path: dir, IsPrimary: ptrBool(true)}},
	})
	testutil.FailErr(t, "create project", err)
	rootID := p.Roots[0].ID

	store := store.NewMemory()
	mgr := NewManager(store, nil, nil, settings.DefaultSessionLimits())
	mgr.SetProjectRegistry(reg)
	mgr.SetWorkerQueue(&stubProjectWorkerLister{tasks: []api.WorkerTask{{
		ID:              "job-1",
		ParentSessionID: "sess-1",
		ProjectID:       p.ID,
		WorkspaceRootID: rootID,
		WorkspacePath:   dir,
		AgentType:       "implementer",
		Status:          api.WorkerStatusRunning,
	}}})

	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create session", err)
	if err := store.UpdateSession(ctx, sess.ID, func(s *api.Session) {
		s.WorkspaceRootID = rootID
		s.Status = api.SessionStatusBusy
	}); err != nil {
		testutil.FailErr(t, "mark busy", err)
	}

	dep, err := mgr.RootDependents(ctx, p.ID, rootID)
	testutil.FailErr(t, "RootDependents", err)
	if len(dep.Workers) != 1 || dep.Workers[0].JobID != "job-1" {
		t.Fatalf("workers = %+v want job-1", dep.Workers)
	}
	if len(dep.Sessions) != 1 || dep.Sessions[0].SessionID != sess.ID {
		t.Fatalf("sessions = %+v want %s", dep.Sessions, sess.ID)
	}
	if !dep.HasAny() {
		t.Fatal("expected dependents")
	}
}

func TestRootDependentsIncludesBusySessionAcrossMultiRootUnion(t *testing.T) {
	ctx := t.Context()
	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{Roots: []project.AttachRootParams{
		{Path: t.TempDir(), IsPrimary: ptrBool(true)}, {Path: t.TempDir()},
	}})
	testutil.FailErr(t, "create project", err)
	store := store.NewMemory()
	mgr := NewManager(store, nil, nil, settings.DefaultSessionLimits())
	mgr.SetProjectRegistry(reg)
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark busy", store.UpdateSession(ctx, sess.ID, func(s *api.Session) {
		s.WorkspaceRootID = p.Roots[0].ID
		s.Status = api.SessionStatusBusy
	}))
	dependents, err := mgr.RootDependents(ctx, p.ID, p.Roots[1].ID)
	testutil.FailErr(t, "root dependents", err)
	if len(dependents.Sessions) != 1 || dependents.Sessions[0].SessionID != sess.ID {
		t.Fatalf("sessions = %+v", dependents.Sessions)
	}
}

func ptrBool(v bool) *bool { return &v }
