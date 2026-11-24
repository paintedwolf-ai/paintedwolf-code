package app

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/app/persistence"
	"github.com/lycaon/lycaon/internal/app/server"
	"github.com/lycaon/lycaon/internal/app/sessions"
	"github.com/lycaon/lycaon/internal/app/workerruntime"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/coordinatorcontrol"
	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	"github.com/lycaon/lycaon/internal/session/processcontrol"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/watch"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWatchProducersAuthorityAndStateLookup(t *testing.T) {
	wireWatchProducers(nil, nil)
	bindWorkerOutcomeWatches(nil, nil)

	database := testdbfixture.Open(t, "watch_producers.db")
	rootDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, rootDir)
	repos := sessionstore.NewSQL(database)

	parent, err := repos.Sessions.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent session", err)
	testdbseed.InsertChildSession(t, database, "child-prod-1", parent.ID)

	_, err = database.ExecContext(t.Context(), `INSERT INTO worker_jobs
		(id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at)
		VALUES ('job-p1', ?, ?, 'child-prod-1', 'implementer', 'completed', 'work', 'work', '2026-05-01T10:00:00Z')`,
		testdbseed.DefaultProjectID, parent.ID)
	testutil.FailErr(t, "insert worker job", err)

	clock := testutil.NewFakeClock(time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC))
	ws := watch.NewStore(database, clock)
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{
		LoopWakeDeps: func() loopwake.LoopDeps {
			return loopwake.LoopDeps{
				GetSession:           repos.Sessions.Get,
				IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
				Limits:               func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
				WorkerCycleIdle:      func(context.Context, *api.Session, string) (bool, error) { return true, nil },
			}
		},
	})
	rt.CoordinatorLoop().Waits.Subscriptions.SetWatchStore(ws)

	bg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	t.Cleanup(func() { _ = bg.Lifecycle.Close(context.Background()) })
	procs := processcontrol.New(repos.Sessions, nil, nil, nil)
	procs.Background = bg

	b := newTestWatchProducerBuilder(t, database, repos, procs, rt)
	wireWatchProducers(b, ws)

	if ok, code := checkWatchAuthority(b, t.Context(), watch.Registration{Owner: watch.Owner{Kind: watch.OwnerPerson}}); !ok || code != "" {
		t.Fatalf("person authority = (%v, %q), want (true, \"\")", ok, code)
	}
	if ok, code := checkWatchAuthority(b, t.Context(), watch.Registration{Owner: watch.Owner{Kind: watch.OwnerSession, SessionID: "missing"}}); ok || code != "SESSION_UNAVAILABLE" {
		t.Fatalf("missing session authority = (%v, %q), want (false, SESSION_UNAVAILABLE)", ok, code)
	}
	if ok, code := checkWatchAuthority(b, t.Context(), watch.Registration{Owner: watch.Owner{Kind: watch.OwnerSession, SessionID: parent.ID, ProjectID: "other-proj"}}); ok || code != "PROJECT_MISMATCH" {
		t.Fatalf("project mismatch authority = (%v, %q), want (false, PROJECT_MISMATCH)", ok, code)
	}
	if ok, code := checkWatchAuthority(b, t.Context(), watch.Registration{Owner: watch.Owner{Kind: watch.OwnerSession, SessionID: parent.ID, ProjectID: testdbseed.DefaultProjectID}}); !ok || code != "" {
		t.Fatalf("valid session authority = (%v, %q), want (true, \"\")", ok, code)
	}

	snap, err := lookupCoordinatorWatchState(b, t.Context(), parent.ID)
	testutil.FailErr(t, "lookup coordinator watch state", err)
	if !snap.HasWorkerCycle || len(snap.SettledWorkers) != 1 || snap.SettledWorkers[0].Status != "complete" {
		t.Fatalf("coordinator watch state = %+v", snap)
	}
	gotParent, _, gotJob := resolveChildWorkerIdentity(b, t.Context(), "child-prod-1")
	if gotParent != parent.ID || gotJob != "job-p1" {
		t.Fatalf("resolveChildWorkerIdentity = (%q, %q), want (%q, job-p1)", gotParent, gotJob, parent.ID)
	}
}

func newTestWatchProducerBuilder(
	t *testing.T,
	database *db.Store,
	repos *sessionstore.Repositories,
	procs *processcontrol.Service,
	rt *coordinator.Runtime,
) *serveBuilder {
	t.Helper()
	res := newRuntimeResources()
	t.Cleanup(func() { _ = (&ServeApp{resources: res}).Close() })
	return &serveBuilder{
		startup: startupBootstrap{ctx: t.Context(), resources: res},
		storage: persistence.Runtime{Database: database, Sessions: repos},
		sessions: &sessions.Runtime{
			Decisions: sessiondecisions.NewSQL(database),
			Manager: &session.Host{
				Processes: procs,
				Coordinator: &coordinatorcontrol.Service{
					Workers: &coordinatorcontrol.Workers{},
					Scans:   &coordinatorcontrol.Scans{},
				},
			},
		},
		workerRuntime: &workerruntime.Runtime{Cancel: &worker.CancelService{}},
		server:        server.Runtime{Coordinator: rt},
	}
}

func TestWatchProducersHooksAndOutcomeBridge(t *testing.T) {
	database := testdbfixture.Open(t, "watch_bridge.db")
	rootDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, rootDir)
	repos := sessionstore.NewSQL(database)

	parent, err := repos.Sessions.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent session", err)
	testdbseed.InsertChildSession(t, database, "child-bridge-1", parent.ID)

	clock := testutil.NewFakeClock(time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC))
	ws := watch.NewStore(database, clock)
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{})
	rt.CoordinatorLoop().Waits.Subscriptions.SetWatchStore(ws)

	res := newRuntimeResources()
	b := &serveBuilder{
		startup: startupBootstrap{ctx: t.Context(), resources: res},
		storage: persistence.Runtime{Database: database, Sessions: repos},
		sessions: &sessions.Runtime{
			Manager: &session.Host{
				Coordinator: &coordinatorcontrol.Service{
					Workers: &coordinatorcontrol.Workers{},
					Scans:   &coordinatorcontrol.Scans{},
				},
			},
		},
		workerRuntime: &workerruntime.Runtime{Cancel: &worker.CancelService{}},
		server:        server.Runtime{Coordinator: rt},
	}
	wireWatchProducers(b, ws)

	mgr := b.sessions.Manager
	mgr.Coordinator.Workers.OnWatchTerminal(t.Context(), parent.ID, "job-1")
	mgr.Coordinator.Workers.OnWatchRunning(t.Context(), parent.ID, "job-1", "fixer", "notice", "notice", "occ-1", "working")
	_ = mgr.Coordinator.Scans.OnWatchScanFinished(t.Context(), parent.ID, api.CodeScan{
		ID:            "scan-1",
		Status:        api.CodeScanStatusComplete,
		Categories:    []api.ScanCategory{"security", "quality"},
		FindingsCount: 1,
	})
	b.workerRuntime.Cancel.OnJobRetired(t.Context(), "child-bridge-1", "job-1")

	bridge := &worker.SessionOutcomeBridge{}
	bindWorkerOutcomeWatches(b, bridge)
	bridge.OnJobRetired(t.Context(), "child-bridge-1", "job-1")
	for _, st := range []string{"complete", "failed", "needs_decision"} {
		_ = bridge.OnWorkerWatchTerminal(t.Context(), parent.ID, "job-"+st, st, "done")
	}

	repochange.Publish(t.Context(), repochange.Event{Kind: repochange.WorktreeChanged, ProjectDir: rootDir})
	sourcefeed.Publish(testdbseed.DefaultProjectID, rootDir, sourcefeed.Notice{})
	testutil.FailErr(t, "close resources", (&ServeApp{resources: res}).Close())
}
