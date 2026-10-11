package session

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/session/chats"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDeleteSessionSharesTurnLifecycleGate(t *testing.T) {
	mem := store.NewMemory()
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild, ProjectID: "p1"}, "p1")
	testutil.FailErr(t, "create session", err)
	turnLock := mgr.Runner.Execution.Prompt.Acquire(sess.ID)
	turnLock.Lock()
	err = mgr.Chats.Delete(t.Context(), sess.ID)
	turnLock.Unlock()
	if !errors.Is(err, chats.ErrSessionBusy) {
		t.Fatalf("DeleteSession = %v, want chats.ErrSessionBusy", err)
	}
	_, err = mem.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "session retained after busy delete", err)
}

func TestDeleteSessionDisposesBackgroundRuntimeBeforeRow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	mem := store.NewMemory()
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	reg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	mgr.SetBackgroundRegistry(reg)
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild, ProjectID: "p1"}, "p1")
	testutil.FailErr(t, "create session", err)
	_, err = reg.StartPipeline(t.Context(), bgprocess.PipelineSpec{
		SessionID: sess.ID,
		ProjectID: "p1",
		Runner:    hostcmd.NewRunner(),
		Mode:      bgprocess.JobModeBackground,
		Request: hostcmd.Request{
			Launch:     lyexec.HostLaunch("session lifecycle test"),
			ProjectDir: t.TempDir(),
			Stages:     []lyexec.Stage{{Name: "sleep", Args: []string{"30"}}},
		},
	})
	testutil.FailErr(t, "start session background process", err)
	testutil.FailErr(t, "delete session", mgr.Chats.Delete(t.Context(), sess.ID))
	if got := reg.Output.List(context.Background(), sess.ID); len(got) != 0 {
		t.Fatalf("background handles retained after delete: %#v", got)
	}
}

func TestDeleteSession_refusesWhileBound(t *testing.T) {
	mem := store.NewMemory()
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	ctx := context.Background()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{
		Posture:   api.SessionPostureBuild,
		ProjectID: "p1",
	}, "p1")
	testutil.FailErr(t, "create", err)
	testutil.FailErr(t, "bind", mem.PutWorktreeBinding(ctx, store.WorktreeBinding{
		SessionID:    sess.ID,
		ProjectID:    "p1",
		RepoID:       "r1",
		Toplevel:     "/repo",
		WorktreePath: "/wt",
		Branch:       "session/x",
		BaseBranch:   "main",
	}))

	err = mgr.Chats.Delete(ctx, sess.ID)
	if !errors.Is(err, chats.ErrSessionWorktreeBound) {
		t.Fatalf("DeleteSession = %v want chats.ErrSessionWorktreeBound", err)
	}
	if _, getErr := mem.Get(ctx, sess.ID); getErr != nil {
		t.Fatalf("session must still exist: %v", getErr)
	}

	testutil.FailErr(t, "unbind", mem.DeleteWorktreeBinding(ctx, sess.ID))
	testutil.FailErr(t, "delete after unbind", mgr.Chats.Delete(ctx, sess.ID))
	if _, getErr := mem.Get(ctx, sess.ID); !errors.Is(getErr, store.ErrSessionNotFound) {
		t.Fatalf("session should be gone: %v", getErr)
	}
}

func TestBoundSession_archivePinRenameUnaffected(t *testing.T) {
	mem := store.NewMemory()
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	ctx := context.Background()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{
		Posture:   api.SessionPostureBuild,
		ProjectID: "p1",
	}, "p1")
	testutil.FailErr(t, "create", err)
	testutil.FailErr(t, "bind", mem.PutWorktreeBinding(ctx, store.WorktreeBinding{
		SessionID:    sess.ID,
		ProjectID:    "p1",
		RepoID:       "r1",
		Toplevel:     "/repo",
		WorktreePath: "/wt",
		Branch:       "session/x",
		BaseBranch:   "main",
	}))

	archived, err := mgr.Chats.SetArchived(ctx, sess.ID, true)
	testutil.FailErr(t, "archive", err)
	if archived.ArchivedAt == nil {
		t.Fatal("expected archived_at")
	}
	unarchived, err := mgr.Chats.SetArchived(ctx, sess.ID, false)
	testutil.FailErr(t, "unarchive", err)
	if unarchived.ArchivedAt != nil {
		t.Fatal("expected cleared archived_at")
	}
	pinned, err := mgr.Chats.SetPinned(ctx, sess.ID, true)
	testutil.FailErr(t, "pin", err)
	if pinned.PinRank == nil {
		t.Fatal("expected pin_rank")
	}
	testutil.FailErr(t, "rename", mem.UpdateSession(ctx, sess.ID, func(s *api.Session) {
		s.Title = "renamed"
		s.UpdatedAt = time.Now().UTC()
	}))
	got, err := mem.Get(ctx, sess.ID)
	testutil.FailErr(t, "get", err)
	if got.Title != "renamed" {
		t.Fatalf("title = %q", got.Title)
	}
}
