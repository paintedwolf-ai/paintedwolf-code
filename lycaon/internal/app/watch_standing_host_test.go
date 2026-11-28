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
	"github.com/lycaon/lycaon/internal/app/workflows"
	awaitstore "github.com/lycaon/lycaon/internal/await"
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
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
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
	observeStandingWatchEvent(t.Context(), b, engine, nil, api.EventEnvelope{Topic: api.EventTopicCheckpoint, Data: cpPending})
	cpResolved, err := json.Marshal(api.CheckpointEvent{SessionID: sess.ID, Status: api.CheckpointStatusApproved})
	testutil.FailErr(t, "marshal resolved checkpoint", err)
	observeStandingWatchEvent(t.Context(), b, engine, nil, api.EventEnvelope{Topic: api.EventTopicCheckpoint, Data: cpResolved})
	sessIdle, err := json.Marshal(api.SessionEvent{ID: sess.ID, Status: api.SessionStatusIdle})
	testutil.FailErr(t, "marshal idle session", err)
	observeStandingWatchEvent(t.Context(), b, engine, nil, api.EventEnvelope{Topic: api.EventTopicSession, Data: sessIdle})
	sessDeleted, err := json.Marshal(api.SessionEvent{ID: "deleted-id", Action: api.SessionEventActionDeleted})
	testutil.FailErr(t, "marshal deleted session", err)
	observeStandingWatchEvent(t.Context(), b, engine, nil, api.EventEnvelope{Topic: api.EventTopicSession, Data: sessDeleted})
	_ = sessionHasOpenCheckpoint(b, t.Context(), sess.ID)

	var wg sync.WaitGroup
	tracker := &standingDispatchTracker{}
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
		out, dErr := dispatchStandingWatchRun(b, t.Context(), &wg, engine, tracker, swExisting, watch.WatchRun{RunID: uuid.NewString()}, "check status")
		testutil.FailErr(t, "dispatch existing chat run", dErr)
		if out.SessionID != sess.ID {
			t.Fatalf("dispatch SessionID = %q, want %q", out.SessionID, sess.ID)
		}
		wg.Wait()
	}

	runOutcome = nil
	swNew := swExisting
	swNew.Target = watch.StandingTarget{Kind: "new_chat"}
	outNew, err := dispatchStandingWatchRun(b, t.Context(), &wg, engine, tracker, swNew, watch.WatchRun{RunID: uuid.NewString()}, "start new run")
	testutil.FailErr(t, "dispatch new chat run", err)
	if outNew.SessionID == "" {
		t.Fatal("expected created SessionID for new_chat run")
	}
	wg.Wait()
}

func TestStandingWatchRunSettlementBarrier(t *testing.T) {
	type barrierCase struct {
		name           string
		hostBlocked    bool
		execErr        error
		openCheckpoint bool
		wantExecuted   bool
		wantStatus     watch.WatchRunStatus
		wantEnded      bool
		wantUnread     bool
	}
	cases := []barrierCase{
		{
			name:         "new_chat_completed",
			wantExecuted: true,
			wantStatus:   watch.RunCompleted,
			wantEnded:    true,
			wantUnread:   true,
		},
		{
			name:         "new_chat_failed",
			execErr:      errors.New("provider boom"),
			wantExecuted: true,
			wantStatus:   watch.RunFailed,
			wantEnded:    true,
		},
		{
			name:         "new_chat_fenced",
			execErr:      spendguard.ErrCeiling,
			wantExecuted: true,
			wantStatus:   watch.RunFenced,
			wantEnded:    true,
		},
		{
			name:         "new_chat_canceled",
			execErr:      lifecycle.ErrStopping,
			wantExecuted: true,
			wantStatus:   watch.RunCanceled,
			wantEnded:    true,
		},
		{
			name:           "new_chat_waiting_for_owner",
			openCheckpoint: true,
			wantExecuted:   true,
			wantStatus:     watch.RunWaitingForOwner,
			wantEnded:      false,
		},
		{
			name:         "new_chat_host_blocked_no_request",
			hostBlocked:  true,
			wantExecuted: false,
			wantStatus:   watch.RunFailed,
			wantEnded:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			database := testdbfixture.Open(t, "watch_standing_barrier_"+tc.name+".db")
			rootDir := t.TempDir()
			testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, rootDir)
			repos := sessionstore.NewSQL(database)

			ownerPerson, err := repos.People.HostOwner(ctx)
			testutil.FailErr(t, "host owner", err)

			clock := testutil.NewFakeClock(time.Date(2026, 5, 1, 14, 0, 0, 0, time.UTC))
			ws := watch.NewStore(database, clock)
			engine := ws.Standing()

			var (
				mu             sync.Mutex
				executed       bool
				executedAuthor string
				executedSessID string
			)
			subs := submissions.New(
				repos.Sessions, repos.Submissions, repos.People, queue.New(),
				lifecycle.New(repos.Sessions), nil, nil, nil,
				&submissionstate.Service{}, &promptstate.MutexRegistry{},
				func(_ context.Context, sid string, in promptinput.Input, lock *promptstate.Lock) (*promptresult.Result, error) {
					defer lock.Unlock()
					mu.Lock()
					executed = true
					executedAuthor = in.AuthorPersonID
					executedSessID = sid
					mu.Unlock()
					if tc.openCheckpoint {
						_, cpErr := database.ExecContext(ctx,
							`INSERT INTO checkpoints (id, session_id, project_id, kind, status, created_at) VALUES (?, ?, ?, 'tool_approval', 'pending', ?)`,
							uuid.NewString(), sid, testdbseed.DefaultProjectID, clock.Now().UTC().Format(time.RFC3339Nano))
						testutil.FailErr(t, "insert pending checkpoint", cpErr)
					}
					return &promptresult.Result{}, tc.execErr
				},
				nil, nil, func(context.Context, string) bool { return tc.hostBlocked },
			)
			b := &serveBuilder{
				startup: startupBootstrap{ctx: ctx},
				storage: persistence.Runtime{Database: database, Sessions: repos},
				sessions: &sessions.Runtime{
					Manager: &session.Host{Submissions: subs},
				},
			}

			var (
				wg                       sync.WaitGroup
				tracker                  = &standingDispatchTracker{}
				sessionIDAtBarrier       string
				persistedLinkBeforeRet   string
				statusAtBarrierBeforeRet string
			)
			engine.SetCallbacks(watch.StandingCallbacks{
				RunDispatcher: func(dCtx context.Context, sw watch.StandingWatch, run watch.WatchRun, promptText string) (watch.StandingRunDispatchOutcome, error) {
					out, dErr := dispatchStandingWatchRun(b, dCtx, &wg, engine, tracker, sw, run, promptText)
					// Barrier: deliberately hold returning out.SessionID to invokeStandingDispatcher
					// until the async HostTurnWithAdmission goroutine and its settlement have finished.
					wg.Wait()
					sessionIDAtBarrier = out.SessionID
					_ = database.QueryRowContext(dCtx,
						`SELECT COALESCE(session_id, ''), status FROM watch_runs WHERE run_id = ?`,
						run.RunID).Scan(&persistedLinkBeforeRet, &statusAtBarrierBeforeRet)
					return out, dErr
				},
			})

			sw, err := engine.Create(ctx, ownerPerson.ID, watch.StandingWatchInput{
				ProjectID: testdbseed.DefaultProjectID,
				Name:      "barrier-" + tc.name,
				Prompt:    "check standing run settlement",
				Conditions: []awaitstore.Condition{{
					Kind:         "timer",
					EveryMinutes: 60,
				}},
				Target: &watch.StandingTarget{Kind: "new_chat"},
			})
			testutil.FailErr(t, "create standing watch", err)

			run, err := engine.RunNow(ctx, ownerPerson.ID, testdbseed.DefaultProjectID, sw.WatchID)
			testutil.FailErr(t, "run standing watch now", err)

			mu.Lock()
			gotExecuted := executed
			gotAuthor := executedAuthor
			gotExecSess := executedSessID
			mu.Unlock()

			t.Logf("run_id=%s session_id=%s executed=%v author=%q link_before_return=%q status_before_return=%q final_status=%q final_ended_at=%v",
				run.RunID, sessionIDAtBarrier, gotExecuted, gotAuthor, persistedLinkBeforeRet, statusAtBarrierBeforeRet, run.Status, run.EndedAt)

			if gotExecuted != tc.wantExecuted {
				t.Fatalf("executed = %v, want %v (run_id=%s session_id=%s final_status=%q)",
					gotExecuted, tc.wantExecuted, run.RunID, sessionIDAtBarrier, run.Status)
			}
			if tc.wantExecuted && gotAuthor != ownerPerson.ID {
				t.Fatalf("executed AuthorPersonID = %q, want %q", gotAuthor, ownerPerson.ID)
			}
			if tc.wantExecuted && gotExecSess != sessionIDAtBarrier {
				t.Fatalf("executed session = %q, want %q", gotExecSess, sessionIDAtBarrier)
			}
			if persistedLinkBeforeRet != sessionIDAtBarrier || sessionIDAtBarrier == "" {
				t.Fatalf("watch_runs.session_id before dispatcher return = %q, want %q", persistedLinkBeforeRet, sessionIDAtBarrier)
			}
			if run.SessionID != sessionIDAtBarrier {
				t.Fatalf("final run.SessionID = %q, want %q", run.SessionID, sessionIDAtBarrier)
			}
			if run.Status != tc.wantStatus {
				t.Fatalf("final run.Status = %q, want %q (status_before_return=%q)", run.Status, tc.wantStatus, statusAtBarrierBeforeRet)
			}
			if tc.wantEnded && run.EndedAt == nil {
				t.Fatalf("final run.EndedAt = nil, want non-nil for terminal status %q", run.Status)
			}
			if !tc.wantEnded && run.EndedAt != nil {
				t.Fatalf("final run.EndedAt = %v, want nil for non-terminal status %q", run.EndedAt, run.Status)
			}
			if tc.wantUnread {
				var seenAt any
				testutil.FailErr(t, "query seen_at", database.QueryRowContext(ctx, `SELECT seen_at FROM sessions WHERE id = ?`, run.SessionID).Scan(&seenAt))
				if seenAt != nil {
					t.Fatalf("completed new_chat run session seen_at = %v, want NULL (unread)", seenAt)
				}
			}

			// Non-resurrection check: once terminal, subsequent session/checkpoint events or settlement calls must not mutate the row.
			if tc.wantEnded {
				cpPending, _ := json.Marshal(api.CheckpointEvent{SessionID: run.SessionID, Status: api.CheckpointStatusPending})
				observeStandingWatchEvent(ctx, b, engine, tracker, api.EventEnvelope{Topic: api.EventTopicCheckpoint, Data: cpPending})
				sessIdle, _ := json.Marshal(api.SessionEvent{ID: run.SessionID, Status: api.SessionStatusIdle})
				observeStandingWatchEvent(ctx, b, engine, tracker, api.EventEnvelope{Topic: api.EventTopicSession, Data: sessIdle})
				_ = engine.MarkWaitingForOwner(ctx, run.SessionID)
				_ = engine.SettleRun(ctx, run.SessionID, watch.RunCompleted)

				var afterStatus string
				testutil.FailErr(t, "re-read terminal run", database.QueryRowContext(ctx, `SELECT status FROM watch_runs WHERE run_id = ?`, run.RunID).Scan(&afterStatus))
				if watch.WatchRunStatus(afterStatus) != tc.wantStatus {
					t.Fatalf("terminal run resurrected/overwritten: status = %q, want %q", afterStatus, tc.wantStatus)
				}
			}
		})
	}
}

func TestObserveStandingWatchEventIgnoresSessionCreatedIdle(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "watch_standing_created_idle.db")
	rootDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, rootDir)
	repos := sessionstore.NewSQL(database)
	ownerPerson, err := repos.People.HostOwner(ctx)
	testutil.FailErr(t, "host owner", err)
	sess, err := repos.Sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	clock := testutil.NewFakeClock(time.Date(2026, 5, 1, 15, 0, 0, 0, time.UTC))
	ws := watch.NewStore(database, clock)
	engine := ws.Standing()
	engine.SetCallbacks(watch.StandingCallbacks{
		RunDispatcher: func(dCtx context.Context, _ watch.StandingWatch, run watch.WatchRun, _ string) (watch.StandingRunDispatchOutcome, error) {
			_ = engine.LinkRunSession(dCtx, run.RunID, sess.ID)
			return watch.StandingRunDispatchOutcome{SessionID: sess.ID}, nil
		},
	})
	sw, err := engine.Create(ctx, ownerPerson.ID, watch.StandingWatchInput{
		ProjectID:  testdbseed.DefaultProjectID,
		Name:       "created-idle-guard",
		Prompt:     "wait for real completion",
		Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 60}},
		Target:     &watch.StandingTarget{Kind: "new_chat"},
	})
	testutil.FailErr(t, "create standing watch", err)
	run, err := engine.RunNow(ctx, ownerPerson.ID, testdbseed.DefaultProjectID, sw.WatchID)
	testutil.FailErr(t, "run now", err)
	if run.Status != watch.RunRunning {
		t.Fatalf("initial run status = %q, want running", run.Status)
	}

	b := &serveBuilder{storage: persistence.Runtime{Database: database, Sessions: repos}}
	createdEv, err := json.Marshal(api.SessionEvent{
		ID:     sess.ID,
		Action: api.SessionEventActionCreated,
		Status: api.SessionStatusIdle,
	})
	testutil.FailErr(t, "marshal created event", err)
	observeStandingWatchEvent(ctx, b, engine, nil, api.EventEnvelope{Topic: api.EventTopicSession, Data: createdEv})

	var status string
	testutil.FailErr(t, "query run status", database.QueryRowContext(ctx, `SELECT status FROM watch_runs WHERE run_id = ?`, run.RunID).Scan(&status))
	if watch.WatchRunStatus(status) != watch.RunRunning {
		t.Fatalf("session.created idle prematurely settled run to %q, want running", status)
	}
}

func TestStandingWatchNewChatAttachesAmbientWorkflowAndPassesAssertRunnable(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "watch_standing_ambient_workflow.db")
	rootDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, rootDir)
	repos := sessionstore.NewSQL(database)
	ownerPerson, err := repos.People.HostOwner(ctx)
	testutil.FailErr(t, "host owner", err)

	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "load workflow manifests", err)
	wfMgr := workflow.NewManager(workflowpersistence.New(database), repos, manifests, nil)

	clock := testutil.NewFakeClock(time.Date(2026, 5, 1, 16, 0, 0, 0, time.UTC))
	ws := watch.NewStore(database, clock)
	engine := ws.Standing()

	var (
		mu          sync.Mutex
		assertedErr error
		executed    bool
	)
	subs := submissions.New(
		repos.Sessions, repos.Submissions, repos.People, queue.New(),
		lifecycle.New(repos.Sessions), nil, nil, nil,
		&submissionstate.Service{}, &promptstate.MutexRegistry{},
		func(runCtx context.Context, sid string, _ promptinput.Input, lock *promptstate.Lock) (*promptresult.Result, error) {
			defer lock.Unlock()
			err := wfMgr.Policy.AssertSessionRunnable(runCtx, sid)
			mu.Lock()
			executed = true
			assertedErr = err
			mu.Unlock()
			return &promptresult.Result{}, err
		},
		nil, nil, func(context.Context, string) bool { return false },
	)
	b := &serveBuilder{
		startup:   startupBootstrap{ctx: ctx},
		storage:   persistence.Runtime{Database: database, Sessions: repos},
		workflows: &workflows.Runtime{Manager: wfMgr},
		sessions: &sessions.Runtime{
			Manager: &session.Host{Submissions: subs},
		},
	}

	var wg sync.WaitGroup
	tracker := &standingDispatchTracker{}
	engine.SetCallbacks(watch.StandingCallbacks{
		RunDispatcher: func(dCtx context.Context, sw watch.StandingWatch, run watch.WatchRun, promptText string) (watch.StandingRunDispatchOutcome, error) {
			out, dErr := dispatchStandingWatchRun(b, dCtx, &wg, engine, tracker, sw, run, promptText)
			wg.Wait()
			return out, dErr
		},
	})

	sw, err := engine.Create(ctx, ownerPerson.ID, watch.StandingWatchInput{
		ProjectID:  testdbseed.DefaultProjectID,
		Name:       "ambient-workflow-check",
		Prompt:     "run on new chat with ambient workflow",
		Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 60}},
		Target:     &watch.StandingTarget{Kind: "new_chat"},
	})
	testutil.FailErr(t, "create standing watch", err)

	run, err := engine.RunNow(ctx, ownerPerson.ID, testdbseed.DefaultProjectID, sw.WatchID)
	testutil.FailErr(t, "run standing watch now", err)

	mu.Lock()
	gotExecuted := executed
	gotAssertErr := assertedErr
	mu.Unlock()
	if !gotExecuted {
		t.Fatal("standing watch turn did not execute")
	}
	if gotAssertErr != nil {
		t.Fatalf("AssertSessionRunnable on new_chat standing run returned error: %v", gotAssertErr)
	}
	if run.Status != watch.RunCompleted {
		t.Fatalf("run.Status = %q, want completed", run.Status)
	}
}

func TestStandingWatchIdleEventDuringFailingTurnDoesNotMisSettleCompleted(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "watch_standing_idle_race.db")
	rootDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, rootDir)
	repos := sessionstore.NewSQL(database)
	ownerPerson, err := repos.People.HostOwner(ctx)
	testutil.FailErr(t, "host owner", err)

	clock := testutil.NewFakeClock(time.Date(2026, 5, 1, 17, 0, 0, 0, time.UTC))
	ws := watch.NewStore(database, clock)
	engine := ws.Standing()

	var b *serveBuilder
	tracker := &standingDispatchTracker{}
	subs := submissions.New(
		repos.Sessions, repos.Submissions, repos.People, queue.New(),
		lifecycle.New(repos.Sessions), nil, nil, nil,
		&submissionstate.Service{}, &promptstate.MutexRegistry{},
		func(runCtx context.Context, sid string, _ promptinput.Input, lock *promptstate.Lock) (*promptresult.Result, error) {
			defer lock.Unlock()
			// Simulate a SessionEvent{Status: idle} (with empty IdleDisposition) arriving
			// while HostTurnWithAdmission is still in-flight before returning its failure.
			idleEv, mErr := json.Marshal(api.SessionEvent{
				ID:     sid,
				Action: api.SessionEventActionUpdated,
				Status: api.SessionStatusIdle,
			})
			testutil.FailErr(t, "marshal idle event", mErr)
			observeStandingWatchEvent(runCtx, b, engine, tracker, api.EventEnvelope{
				Topic: api.EventTopicSession,
				Data:  idleEv,
			})
			return nil, errors.New("coordinator turn failed")
		},
		nil, nil, func(context.Context, string) bool { return false },
	)
	b = &serveBuilder{
		startup: startupBootstrap{ctx: ctx},
		storage: persistence.Runtime{Database: database, Sessions: repos},
		sessions: &sessions.Runtime{
			Manager: &session.Host{Submissions: subs},
		},
	}

	var wg sync.WaitGroup
	engine.SetCallbacks(watch.StandingCallbacks{
		RunDispatcher: func(dCtx context.Context, sw watch.StandingWatch, run watch.WatchRun, promptText string) (watch.StandingRunDispatchOutcome, error) {
			out, dErr := dispatchStandingWatchRun(b, dCtx, &wg, engine, tracker, sw, run, promptText)
			wg.Wait()
			return out, dErr
		},
	})

	sw, err := engine.Create(ctx, ownerPerson.ID, watch.StandingWatchInput{
		ProjectID:  testdbseed.DefaultProjectID,
		Name:       "idle-race-during-failure",
		Prompt:     "fail turn after idle event",
		Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 60}},
		Target:     &watch.StandingTarget{Kind: "new_chat"},
	})
	testutil.FailErr(t, "create standing watch", err)

	run, err := engine.RunNow(ctx, ownerPerson.ID, testdbseed.DefaultProjectID, sw.WatchID)
	testutil.FailErr(t, "run standing watch now", err)
	if run.Status != watch.RunFailed {
		t.Fatalf("run.Status = %q, want failed (idle event during in-flight failing turn must not mis-settle as completed)", run.Status)
	}
}

func TestStandingWatchLinkRunSessionFailureAbortsTurnLaunch(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "watch_standing_link_failure.db")
	rootDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, rootDir)
	repos := sessionstore.NewSQL(database)
	ownerPerson, err := repos.People.HostOwner(ctx)
	testutil.FailErr(t, "host owner", err)

	// Fail only the pre-dispatch LinkRunSession UPDATE (running row with NULL session_id),
	// while still allowing invokeStandingDispatcher's terminal RunFailed settlement UPDATE.
	_, err = database.ExecContext(ctx, `
		CREATE TRIGGER fail_standing_link_for_test
		BEFORE UPDATE OF session_id ON watch_runs
		WHEN OLD.session_id IS NULL AND NEW.session_id IS NOT NULL AND NEW.status = 'running'
		BEGIN
			SELECT RAISE(ABORT, 'injected link failure');
		END;`)
	testutil.FailErr(t, "create link failure trigger", err)

	clock := testutil.NewFakeClock(time.Date(2026, 5, 1, 18, 0, 0, 0, time.UTC))
	ws := watch.NewStore(database, clock)
	engine := ws.Standing()

	var (
		mu       sync.Mutex
		executed bool
	)
	subs := submissions.New(
		repos.Sessions, repos.Submissions, repos.People, queue.New(),
		lifecycle.New(repos.Sessions), nil, nil, nil,
		&submissionstate.Service{}, &promptstate.MutexRegistry{},
		func(_ context.Context, _ string, _ promptinput.Input, lock *promptstate.Lock) (*promptresult.Result, error) {
			defer lock.Unlock()
			mu.Lock()
			executed = true
			mu.Unlock()
			return &promptresult.Result{}, nil
		},
		nil, nil, func(context.Context, string) bool { return false },
	)
	b := &serveBuilder{
		startup: startupBootstrap{ctx: ctx},
		storage: persistence.Runtime{Database: database, Sessions: repos},
		sessions: &sessions.Runtime{
			Manager: &session.Host{Submissions: subs},
		},
	}

	var wg sync.WaitGroup
	tracker := &standingDispatchTracker{}
	var dispatchedSessionID string
	engine.SetCallbacks(watch.StandingCallbacks{
		RunDispatcher: func(dCtx context.Context, sw watch.StandingWatch, run watch.WatchRun, promptText string) (watch.StandingRunDispatchOutcome, error) {
			out, dErr := dispatchStandingWatchRun(b, dCtx, &wg, engine, tracker, sw, run, promptText)
			dispatchedSessionID = out.SessionID
			wg.Wait()
			return out, dErr
		},
	})

	sw, err := engine.Create(ctx, ownerPerson.ID, watch.StandingWatchInput{
		ProjectID:  testdbseed.DefaultProjectID,
		Name:       "link-failure-abort",
		Prompt:     "must not launch when session link fails",
		Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 60}},
		Target:     &watch.StandingTarget{Kind: "new_chat"},
	})
	testutil.FailErr(t, "create standing watch", err)

	run, err := engine.RunNow(ctx, ownerPerson.ID, testdbseed.DefaultProjectID, sw.WatchID)
	testutil.FailErr(t, "run standing watch now", err)

	mu.Lock()
	gotExecuted := executed
	mu.Unlock()
	if gotExecuted {
		t.Fatal("standing watch turn executed despite LinkRunSession failure")
	}
	if tracker.isActive(dispatchedSessionID) {
		t.Fatalf("tracker still active for session %q after LinkRunSession failure", dispatchedSessionID)
	}
	if run.Status != watch.RunFailed {
		t.Fatalf("run.Status = %q, want failed", run.Status)
	}
	if run.EndedAt == nil {
		t.Fatal("run.EndedAt = nil, want non-nil on failed settlement")
	}
}

func TestStandingWatchExistingChatBecomesBusyBetweenEligibilityAndAdmission(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "watch_standing_existing_busy_race.db")
	rootDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, rootDir)
	repos := sessionstore.NewSQL(database)
	ownerPerson, err := repos.People.HostOwner(ctx)
	testutil.FailErr(t, "host owner", err)
	sess, err := repos.Sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create target session", err)

	clock := testutil.NewFakeClock(time.Date(2026, 5, 1, 19, 0, 0, 0, time.UTC))
	ws := watch.NewStore(database, clock)
	engine := ws.Standing()

	var (
		mu        sync.Mutex
		execCount int
	)
	subs := submissions.New(
		repos.Sessions, repos.Submissions, repos.People, queue.New(),
		lifecycle.New(repos.Sessions), nil, nil, nil,
		&submissionstate.Service{}, &promptstate.MutexRegistry{},
		func(_ context.Context, _ string, _ promptinput.Input, lock *promptstate.Lock) (*promptresult.Result, error) {
			defer lock.Unlock()
			mu.Lock()
			execCount++
			mu.Unlock()
			return &promptresult.Result{}, nil
		},
		nil, nil, func(context.Context, string) bool { return false },
	)
	b := &serveBuilder{
		startup: startupBootstrap{ctx: ctx},
		storage: persistence.Runtime{Database: database, Sessions: repos},
		sessions: &sessions.Runtime{
			Manager: &session.Host{Submissions: subs},
		},
	}

	var wg sync.WaitGroup
	tracker := &standingDispatchTracker{}
	engine.SetCallbacks(watch.StandingCallbacks{
		RunDispatcher: func(dCtx context.Context, sw watch.StandingWatch, run watch.WatchRun, promptText string) (watch.StandingRunDispatchOutcome, error) {
			return dispatchStandingWatchRun(b, dCtx, &wg, engine, tracker, sw, run, promptText)
		},
	})

	sw, err := engine.Create(ctx, ownerPerson.ID, watch.StandingWatchInput{
		ProjectID:  testdbseed.DefaultProjectID,
		Name:       "existing-chat-busy-race",
		Prompt:     "defer when target chat becomes busy",
		Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 60}},
		Target:     &watch.StandingTarget{Kind: "existing_chat", SessionID: sess.ID},
	})
	testutil.FailErr(t, "create standing watch", err)

	// Hold the session's dispatch lane so AmbientWakeEligible succeeds while the session is idle,
	// then mark the session busy before the async goroutine can acquire LockDispatch and admit.
	dispatchLock := subs.DispatchLock(sess.ID)
	dispatchLock.Lock()
	run, err := engine.RunNow(ctx, ownerPerson.ID, testdbseed.DefaultProjectID, sw.WatchID)
	testutil.FailErr(t, "run standing watch now", err)
	testutil.FailErr(t, "mark session busy before admission", repos.Sessions.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))
	dispatchLock.Unlock()
	wg.Wait()

	mu.Lock()
	firstExec := execCount
	mu.Unlock()
	if firstExec != 0 {
		t.Fatalf("execCount = %d while target chat was busy, want 0", firstExec)
	}
	if tracker.isActive(sess.ID) {
		t.Fatalf("tracker still active for session %q after busy requeue", sess.ID)
	}
	var statusAfterBusy, startedAtAfterBusy string
	testutil.FailErr(t, "query requeued run", database.QueryRowContext(ctx,
		`SELECT status, COALESCE(started_at, '') FROM watch_runs WHERE run_id = ?`, run.RunID).Scan(&statusAfterBusy, &startedAtAfterBusy))
	if watch.WatchRunStatus(statusAfterBusy) != watch.RunPending || startedAtAfterBusy != "" {
		t.Fatalf("after busy race: status=%q started_at=%q, want pending and empty started_at", statusAfterBusy, startedAtAfterBusy)
	}

	// Once the target chat returns to idle, observeStandingWatchEvent triggers SettleRun -> DispatchProjectRuns
	// and the requeued pending run executes and completes cleanly.
	testutil.FailErr(t, "mark session idle", repos.Sessions.SetSessionStatus(ctx, sess.ID, api.SessionStatusIdle))
	idleEv, err := json.Marshal(api.SessionEvent{
		ID:              sess.ID,
		Action:          api.SessionEventActionUpdated,
		Status:          api.SessionStatusIdle,
		IdleDisposition: api.SessionIdleDispositionCompleted,
	})
	testutil.FailErr(t, "marshal idle event", err)
	observeStandingWatchEvent(ctx, b, engine, tracker, api.EventEnvelope{Topic: api.EventTopicSession, Data: idleEv})
	wg.Wait()

	mu.Lock()
	secondExec := execCount
	mu.Unlock()
	if secondExec != 1 {
		t.Fatalf("execCount after idle retry = %d, want 1", secondExec)
	}
	var finalStatus string
	testutil.FailErr(t, "query final retried run", database.QueryRowContext(ctx,
		`SELECT status FROM watch_runs WHERE run_id = ?`, run.RunID).Scan(&finalStatus))
	if watch.WatchRunStatus(finalStatus) != watch.RunCompleted {
		t.Fatalf("final status after idle retry = %q, want completed", finalStatus)
	}
}
