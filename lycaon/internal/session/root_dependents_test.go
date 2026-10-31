package session

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
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
	mgr := NewHost(store, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
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

	dep, err := mgr.ProjectControl.RootDependents(ctx, p.ID, rootID)
	testutil.FailErr(t, "projectcontrol.RootDependents", err)
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
	mgr := NewHost(store, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.SetProjectRegistry(reg)
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark busy", store.UpdateSession(ctx, sess.ID, func(s *api.Session) {
		s.WorkspaceRootID = p.Roots[0].ID
		s.Status = api.SessionStatusBusy
	}))
	dependents, err := mgr.ProjectControl.RootDependents(ctx, p.ID, p.Roots[1].ID)
	testutil.FailErr(t, "root dependents", err)
	if len(dependents.Sessions) != 1 || dependents.Sessions[0].SessionID != sess.ID {
		t.Fatalf("sessions = %+v", dependents.Sessions)
	}
}

func TestRootDetachStopsOnlyStrandedCommandsAndQueuesCompletionAfterAbort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	ctx := t.Context()
	rootADir := t.TempDir()
	nestedADir := filepath.Join(rootADir, "services", "api")
	testutil.FailErr(t, "mkdir nestedA", os.MkdirAll(nestedADir, 0o755))
	rootBDir := t.TempDir()

	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{Roots: []project.AttachRootParams{
		{Path: rootADir, IsPrimary: ptrBool(true)},
		{Path: rootBDir},
	}})
	testutil.FailErr(t, "create project", err)
	rootA := p.Roots[0]
	rootB := p.Roots[1]

	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.SetProjectRegistry(reg)
	testutil.FailErr(t, "install anchor registry", mgr.Coordinator.Guidance.InstallAnchorRegistry())

	bgReg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{
		Complete: func(cctx context.Context, c bgprocess.Completion) {
			mgr.Processes.HandleCommandCompletion(cctx, c)
		},
	})
	t.Cleanup(func() { _ = bgReg.Lifecycle.Close(context.Background()) })
	mgr.SetBackgroundRegistry(bgReg)

	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark session busy on rootA", st.UpdateSession(ctx, sess.ID, func(s *api.Session) {
		s.WorkspaceRootID = rootA.ID
		s.Status = api.SessionStatusBusy
	}))

	runner := hostcmd.NewRunner()
	handleA, err := bgReg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: sess.ID, ProjectID: p.ID, Runner: runner, Mode: bgprocess.JobModeBackground,
		OriginTool: "command",
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("rootA server"),
			ProjectDir: nestedADir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
	testutil.FailErr(t, "start handleA under rootA", err)

	handleB, err := bgReg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: sess.ID, ProjectID: p.ID, Runner: runner, Mode: bgprocess.JobModeBackground,
		OriginTool: "command",
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("rootB server"),
			ProjectDir: rootBDir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"29"}}},
		},
	})
	testutil.FailErr(t, "start handleB under rootB", err)

	dependents, err := mgr.ProjectControl.RootDependents(ctx, p.ID, rootA.ID)
	testutil.FailErr(t, "RootDependents(rootA)", err)
	if len(dependents.BackgroundProcesses) != 1 || dependents.BackgroundProcesses[0].Handle != handleA {
		t.Fatalf("BackgroundProcesses = %+v, want only [%s]", dependents.BackgroundProcesses, handleA)
	}
	if len(dependents.Sessions) != 1 || dependents.Sessions[0].SessionID != sess.ID {
		t.Fatalf("Sessions = %+v, want [%s]", dependents.Sessions, sess.ID)
	}

	// Simulate a foreground command under rootA that promotes to background before teardown finishes.
	promotedA, err := bgReg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: sess.ID, ProjectID: p.ID, Runner: runner, Mode: bgprocess.JobModeAwaited,
		OriginTool: "command",
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("promoted rootA command"),
			ProjectDir: rootADir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"28"}}},
		},
	})
	testutil.FailErr(t, "start promotedA", err)
	testutil.FailErr(t, "promote promotedA", bgReg.Output.Promote(ctx, sess.ID, promotedA))

	testutil.FailErr(t, "ForceCancelForRootDetach",
		mgr.ProjectControl.ForceCancelForRootDetach(ctx, p.ID, rootA.ID, dependents, rootA.Path))
	mgr.ProjectControl.ReassignSessionsAfterRootDetach(ctx, p.ID, rootA.ID, []projectroot.RootRef{
		{ID: rootB.ID, Path: rootB.Path, IsPrimary: true},
	})

	for _, h := range []string{handleA, promotedA} {
		snap, err := bgReg.Snapshot(ctx, sess.ID, h, 0)
		testutil.FailErr(t, "snapshot "+h, err)
		if !snap.HasExit || snap.TerminationReason != bgprocess.TerminationRootDetached {
			t.Fatalf("handle %s = (hasExit=%v, reason=%q), want (true, root_detached)", h, snap.HasExit, snap.TerminationReason)
		}
	}

	snapB, err := bgReg.Snapshot(ctx, sess.ID, handleB, 0)
	testutil.FailErr(t, "snapshot handleB", err)
	if snapB.HasExit {
		t.Fatal("expected handleB under rootB to keep running after detaching rootA")
	}

	// Because StopUnderRoot runs after Stops.Abort, the completion notice for the
	// stopped rootA command must remain queued for the next turn.
	if kickID, ok := mgr.Runner.Coordinator.Kicks().PeekPendingKickID(sess.ID); !ok || kickID != "coordinator-process-finished" {
		t.Fatalf("pending kick after root detach = (%q, %v), want coordinator-process-finished", kickID, ok)
	}
}

func TestRootDetachIdleAndArchivedChatsInventoryAndStopBackgroundCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	ctx := t.Context()
	rootADir := t.TempDir()
	rootBDir := t.TempDir()

	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{Roots: []project.AttachRootParams{
		{Path: rootADir, IsPrimary: ptrBool(true)},
		{Path: rootBDir},
	}})
	testutil.FailErr(t, "create project", err)
	rootA := p.Roots[0]

	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.SetProjectRegistry(reg)

	bgReg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	t.Cleanup(func() { _ = bgReg.Lifecycle.Close(context.Background()) })
	mgr.SetBackgroundRegistry(bgReg)

	idleSess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create idle session", err)

	archivedSess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create archived session", err)
	now := time.Now().UTC()
	testutil.FailErr(t, "archive session", st.UpdateSession(ctx, archivedSess.ID, func(s *api.Session) {
		s.ArchivedAt = &now
	}))

	runner := hostcmd.NewRunner()
	idleHandleA, err := bgReg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: idleSess.ID, ProjectID: p.ID, Runner: runner, Mode: bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("idle rootA server"),
			ProjectDir: rootADir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
	testutil.FailErr(t, "start idleHandleA", err)

	archivedHandleA, err := bgReg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: archivedSess.ID, ProjectID: p.ID, Runner: runner, Mode: bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("archived rootA server"),
			ProjectDir: rootADir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"29"}}},
		},
	})
	testutil.FailErr(t, "start archivedHandleA", err)

	idleHandleB, err := bgReg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID: idleSess.ID, ProjectID: p.ID, Runner: runner, Mode: bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch:     exec.HostLaunch("idle rootB server"),
			ProjectDir: rootBDir,
			Stages:     []exec.Stage{{Name: "sleep", Args: []string{"28"}}},
		},
	})
	testutil.FailErr(t, "start idleHandleB", err)

	dependents, err := mgr.ProjectControl.RootDependents(ctx, p.ID, rootA.ID)
	testutil.FailErr(t, "RootDependents(rootA)", err)
	if !dependents.HasAny() {
		t.Fatal("expected RootDependents.HasAny() to be true for idle and archived background commands")
	}
	if len(dependents.Sessions) != 0 {
		t.Fatalf("expected 0 busy sessions in RootDependents, got %+v", dependents.Sessions)
	}
	if len(dependents.BackgroundProcesses) != 2 {
		t.Fatalf("expected 2 background processes in RootDependents, got %+v", dependents.BackgroundProcesses)
	}

	testutil.FailErr(t, "ForceCancelForRootDetach",
		mgr.ProjectControl.ForceCancelForRootDetach(ctx, p.ID, rootA.ID, dependents, rootA.Path))

	for _, item := range []struct {
		sess   string
		handle string
	}{
		{idleSess.ID, idleHandleA},
		{archivedSess.ID, archivedHandleA},
	} {
		snap, err := bgReg.Snapshot(ctx, item.sess, item.handle, 0)
		testutil.FailErr(t, "snapshot "+item.handle, err)
		if !snap.HasExit || snap.TerminationReason != bgprocess.TerminationRootDetached {
			t.Fatalf("handle %s = (hasExit=%v, reason=%q), want (true, root_detached)", item.handle, snap.HasExit, snap.TerminationReason)
		}
	}

	snapB, err := bgReg.Snapshot(ctx, idleSess.ID, idleHandleB, 0)
	testutil.FailErr(t, "snapshot idleHandleB", err)
	if snapB.HasExit {
		t.Fatal("expected idleHandleB under rootB to remain running")
	}
}

func ptrBool(v bool) *bool { return &v }
