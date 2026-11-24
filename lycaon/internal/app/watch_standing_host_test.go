package app

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/app/eventing"
	"github.com/lycaon/lycaon/internal/app/persistence"
	"github.com/lycaon/lycaon/internal/app/providers"
	"github.com/lycaon/lycaon/internal/app/server"
	"github.com/lycaon/lycaon/internal/app/sessions"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/queue"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/chats"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	sessionlimits "github.com/lycaon/lycaon/internal/session/limits"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/promptstate"
	"github.com/lycaon/lycaon/internal/session/spendguard"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/submissions"
	"github.com/lycaon/lycaon/internal/session/submissionstate"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/watch"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubCostTracker struct {
	unpriced bool
	err      error
}

func (s stubCostTracker) RecordUsage(context.Context, cost.UsageEvent) error { return nil }
func (s stubCostTracker) Estimate(context.Context, string, string, cost.TokenUsage) (cost.CostEstimate, error) {
	return cost.CostEstimate{Unpriced: s.unpriced}, s.err
}
func (s stubCostTracker) Summary(context.Context, api.CostScope, string, string) (api.CostSummary, error) {
	return api.CostSummary{}, nil
}
func (s stubCostTracker) ProjectReport(context.Context, string, cost.ReportQuery) (api.ProjectCostReport, error) {
	return api.ProjectCostReport{}, nil
}
func (s stubCostTracker) ClaimSpendWarning(context.Context, string, float64) (bool, error) {
	return false, nil
}
func (s stubCostTracker) ClearSpendWarning(context.Context, string) error { return nil }

func TestStandingWatchHostChecksAndDefaults(t *testing.T) {
	wireStandingWatchHost(nil, nil)
	if (*serveBuilder)(nil).watchStore() != nil {
		t.Fatal("nil builder returned watchStore")
	}

	database := testdbfixture.Open(t, "watch_standing_host.db")
	rootDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, rootDir)
	repos := sessionstore.NewSQL(database)

	ownerPerson, err := repos.People.HostOwner(t.Context())
	testutil.FailErr(t, "host owner", err)
	sess, err := repos.Sessions.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	clock := testutil.NewFakeClock(time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC))
	ws := watch.NewStore(database, clock)
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{})
	rt.CoordinatorLoop().Waits.Subscriptions.SetWatchStore(ws)

	subs := submissions.New(
		repos.Sessions, repos.Submissions, repos.People, queue.New(),
		lifecycle.New(repos.Sessions), nil, nil, nil,
		&submissionstate.Service{}, &promptstate.MutexRegistry{},
		nil, nil, nil, nil,
	)
	res := newRuntimeResources()
	b := newTestStandingHostBuilder(t, database, repos, subs, rt, res)
	wireStandingWatchHost(b, ws)
	if b.watchStore() != ws {
		t.Fatal("expected wired watchStore")
	}

	if p, m := splitProviderModelID("openai:gpt-5"); p != "openai" || m != "gpt-5" {
		t.Fatalf("splitProviderModelID = (%q, %q)", p, m)
	}
	if ok, err := standingModelPriceCheck(b, t.Context(), testdbseed.DefaultProjectID, "openai:gpt-5"); err != nil || !ok {
		t.Fatalf("priced model = (%v, %v), want (true, nil)", ok, err)
	}
	b.providers.Costs = stubCostTracker{err: errors.New("unknown model")}
	if ok, _ := standingModelPriceCheck(b, t.Context(), testdbseed.DefaultProjectID, "openai:unknown"); ok {
		t.Fatal("expected unpriced error to reject model")
	}

	if ok, err := standingPersonAccessCheck(b, t.Context(), testdbseed.DefaultProjectID, ownerPerson.ID); err != nil || !ok {
		t.Fatalf("owner access = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, _ := standingPersonAccessCheck(b, t.Context(), testdbseed.DefaultProjectID, "missing-person"); ok {
		t.Fatal("missing person granted access")
	}

	if exists, busy, err := standingSessionTargetCheck(b, t.Context(), testdbseed.DefaultProjectID, sess.ID); err != nil || !exists || busy {
		t.Fatalf("idle target check = (%v, %v, %v), want (true, false, nil)", exists, busy, err)
	}
	if exists, _, _ := standingSessionTargetCheck(b, t.Context(), testdbseed.DefaultProjectID, "missing"); exists {
		t.Fatal("missing session reported existing")
	}

	profile, _, tz, auth := standingProjectDefaults(b, t.Context(), testdbseed.DefaultProjectID)
	if profile != "default" || tz == "" || auth.ProjectDir != rootDir || len(auth.Roots) == 0 {
		t.Fatalf("standingProjectDefaults = (%q, %q, %+v)", profile, tz, auth)
	}
	testutil.FailErr(t, "close resources", (&ServeApp{resources: res}).Close())
}

func newTestStandingHostBuilder(
	t *testing.T,
	database *db.Store,
	repos *sessionstore.Repositories,
	subs *submissions.Service,
	rt *coordinator.Runtime,
	res *runtimeResources,
) *serveBuilder {
	t.Helper()
	hub := events.NewMemoryHub()
	return &serveBuilder{
		startup:   startupBootstrap{ctx: t.Context(), resources: res},
		storage:   persistence.Runtime{Database: database, Sessions: repos},
		providers: providers.Runtime{Costs: stubCostTracker{}},
		events:    &eventing.Runtime{Hub: hub, Outbox: eventoutbox.New(database, hub)},
		sessions: &sessions.Runtime{
			Manager: &session.Host{
				Chats:       &chats.Service{},
				Limits:      &sessionlimits.Service{},
				Submissions: subs,
			},
		},
		server: server.Runtime{Coordinator: rt},
	}
}

func TestStandingWatchHostEventsAndDispatch(t *testing.T) {
	database := testdbfixture.Open(t, "watch_standing_dispatch.db")
	rootDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, rootDir)
	repos := sessionstore.NewSQL(database)

	ownerPerson, err := repos.People.HostOwner(t.Context())
	testutil.FailErr(t, "host owner", err)
	sess, err := repos.Sessions.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create target session", err)

	clock := testutil.NewFakeClock(time.Date(2026, 5, 1, 13, 0, 0, 0, time.UTC))
	ws := watch.NewStore(database, clock)
	engine := ws.Standing()

	var runOutcome error
	subs := submissions.New(
		repos.Sessions, repos.Submissions, repos.People, queue.New(),
		lifecycle.New(repos.Sessions), nil, nil, nil,
		&submissionstate.Service{}, &promptstate.MutexRegistry{},
		func(_ context.Context, _ string, _ promptinput.Input, lock *promptstate.Lock) (*promptresult.Result, error) {
			defer lock.Unlock()
			return &promptresult.Result{}, runOutcome
		},
		nil, nil, func(context.Context, string) bool { return false },
	)
	b := &serveBuilder{
		startup: startupBootstrap{ctx: t.Context()},
		storage: persistence.Runtime{Database: database, Sessions: repos},
		sessions: &sessions.Runtime{
			Manager: &session.Host{Submissions: subs},
		},
	}

	cpPending, err := json.Marshal(api.CheckpointEvent{SessionID: sess.ID, Status: api.CheckpointStatusPending})
	testutil.FailErr(t, "marshal pending checkpoint", err)
	observeStandingWatchEvent(t.Context(), b, engine, api.EventEnvelope{Topic: api.EventTopicCheckpoint, Data: cpPending})
	cpResolved, err := json.Marshal(api.CheckpointEvent{SessionID: sess.ID, Status: api.CheckpointStatusApproved})
	testutil.FailErr(t, "marshal resolved checkpoint", err)
	observeStandingWatchEvent(t.Context(), b, engine, api.EventEnvelope{Topic: api.EventTopicCheckpoint, Data: cpResolved})
	sessIdle, err := json.Marshal(api.SessionEvent{ID: sess.ID, Status: api.SessionStatusIdle})
	testutil.FailErr(t, "marshal idle session", err)
	observeStandingWatchEvent(t.Context(), b, engine, api.EventEnvelope{Topic: api.EventTopicSession, Data: sessIdle})
	sessDeleted, err := json.Marshal(api.SessionEvent{ID: "deleted-id", Action: api.SessionEventActionDeleted})
	testutil.FailErr(t, "marshal deleted session", err)
	observeStandingWatchEvent(t.Context(), b, engine, api.EventEnvelope{Topic: api.EventTopicSession, Data: sessDeleted})
	_ = sessionHasOpenCheckpoint(b, t.Context(), sess.ID)

	var wg sync.WaitGroup
	swExisting := watch.StandingWatch{
		WatchID:       uuid.NewString(),
		ProjectID:     testdbseed.DefaultProjectID,
		OwnerPersonID: ownerPerson.ID,
		Name:          "existing-watch",
		ModelID:       "openai:gpt-5",
		Target:        watch.StandingTarget{Kind: "existing_chat", SessionID: sess.ID},
	}
	for _, tc := range []error{nil, spendguard.ErrCeiling, lifecycle.ErrStopping, errors.New("provider error")} {
		runOutcome = tc
		out, dErr := dispatchStandingWatchRun(b, t.Context(), &wg, engine, swExisting, watch.WatchRun{RunID: uuid.NewString()}, "check status")
		testutil.FailErr(t, "dispatch existing chat run", dErr)
		if out.SessionID != sess.ID {
			t.Fatalf("dispatch SessionID = %q, want %q", out.SessionID, sess.ID)
		}
		wg.Wait()
	}

	runOutcome = nil
	swNew := swExisting
	swNew.Target = watch.StandingTarget{Kind: "new_chat"}
	outNew, err := dispatchStandingWatchRun(b, t.Context(), &wg, engine, swNew, watch.WatchRun{RunID: uuid.NewString()}, "start new run")
	testutil.FailErr(t, "dispatch new chat run", err)
	if outNew.SessionID == "" {
		t.Fatal("expected created SessionID for new_chat run")
	}
	wg.Wait()
}
