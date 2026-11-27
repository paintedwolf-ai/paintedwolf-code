package stopping

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/queue"
	"github.com/lycaon/lycaon/internal/session/chats"
	"github.com/lycaon/lycaon/internal/session/draftqueue"
	"github.com/lycaon/lycaon/internal/session/execution"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubWatchStop struct {
	fenced       [][]string
	reconciled   [][]string
	reconcileErr error
}

func (s *stubWatchStop) FenceWatches(_ context.Context, sessionIDs []string) error {
	s.fenced = append(s.fenced, slices.Clone(sessionIDs))
	return nil
}

func (s *stubWatchStop) ReconcileStoppedWatches(_ context.Context, sessionIDs []string) (int, error) {
	s.reconciled = append(s.reconciled, slices.Clone(sessionIDs))
	return len(sessionIDs), s.reconcileErr
}

func TestStopSessionTreeFencesAndReconcilesWatches(t *testing.T) {
	var nilSvc *Service
	nilSvc.SetWatchStop(&stubWatchStop{})

	db := testdbfixture.Open(t, "stopping_watch.db")
	testdbseed.InsertProjectRoot(t, db, testdbseed.DefaultProjectID, t.TempDir())
	repos := store.NewSQL(db)

	root, err := repos.Sessions.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create root session", err)
	testdbseed.InsertChildSession(t, db, "child-stop-1", root.ID)

	q := queue.New()
	gate := lifecycle.New(repos.Sessions)
	lifetime := execution.NewLifetime(gate)
	drafts := draftqueue.New(repos.Sessions, repos.Submissions, q, func(context.Context, string) {})
	status := execution.NewStatus(repos.Sessions, repos.Transcript)
	svc := New(repos.Sessions, repos.Submissions, gate, lifetime, &chats.Service{}, q, drafts, &execution.Recovery{}, status)

	ws := &stubWatchStop{}
	svc.SetWatchStop(ws)

	testutil.FailErr(t, "mark root busy", repos.Sessions.SetSessionStatus(t.Context(), root.ID, api.SessionStatusBusy))
	testutil.FailErr(t, "abort session tree", svc.Abort(t.Context(), root.ID, "user stop"))

	if len(ws.fenced) != 2 || len(ws.reconciled) != 1 {
		t.Fatalf("watch stop calls fenced=%d reconciled=%d, want 2 and 1", len(ws.fenced), len(ws.reconciled))
	}
	if !slices.Contains(ws.reconciled[0], root.ID) || !slices.Contains(ws.reconciled[0], "child-stop-1") {
		t.Fatalf("reconciled IDs = %v, want root %q and child-stop-1", ws.reconciled[0], root.ID)
	}
	afterOK, err := repos.Sessions.Get(t.Context(), root.ID)
	testutil.FailErr(t, "get root after stop", err)
	if afterOK.Status != api.SessionStatusIdle {
		t.Fatalf("root status = %q, want idle", afterOK.Status)
	}

	wsFail := &stubWatchStop{reconcileErr: errors.New("db busy")}
	svc.SetWatchStop(wsFail)
	testutil.FailErr(t, "mark root busy again", repos.Sessions.SetSessionStatus(t.Context(), root.ID, api.SessionStatusBusy))
	if err := svc.Abort(t.Context(), root.ID, "user stop"); err == nil {
		t.Fatal("expected reconcile failure from Abort")
	}
	afterFail, err := repos.Sessions.Get(t.Context(), root.ID)
	testutil.FailErr(t, "get root after failed reconcile", err)
	if afterFail.Status != api.SessionStatusBusy {
		t.Fatalf("root status after failed reconcile = %q, want busy", afterFail.Status)
	}
}
