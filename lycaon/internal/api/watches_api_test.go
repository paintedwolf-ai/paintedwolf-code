package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/people/personactions"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testtool"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/toolstest"
	"github.com/lycaon/lycaon/internal/watch"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type fixedTestClock struct{ now time.Time }

func (c *fixedTestClock) Now() time.Time { return c.now }

type watchAPITestEnv struct {
	srv         *Server
	watches     *watch.Store
	clock       *fixedTestClock
	hub         *events.MemoryHub
	outbox      *eventoutbox.Outbox
	store       *store.Repositories
	projects    project.Registry
	sessions    *session.Host
	owner       people.Person
	unpricedMdl string
}

func newWatchAPITestEnv(t *testing.T) *watchAPITestEnv {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	st := store.NewSQL(sqlDB)
	projReg := project.NewSQLRegistry(sqlDB)
	hub := events.NewMemoryHub()
	publisher := &events.Publisher{Hub: hub}
	outbox := eventoutbox.New(sqlDB, hub)
	ctx, cancelOutbox := context.WithCancel(t.Context())
	outbox.Start(ctx)
	t.Cleanup(func() {
		cancelOutbox()
		_ = outbox.Close()
	})

	clock := &fixedTestClock{now: time.Date(2026, 11, 24, 16, 0, 0, 0, time.UTC)}
	watchStore := watch.NewStore(sqlDB, clock)
	watchStore.SetEventOutbox(outbox)

	unpricedModel := "mock:unpriced-special"
	watchStore.Standing().SetCallbacks(watch.StandingCallbacks{
		ModelPriceCheck: func(_ context.Context, _, modelID string) (bool, error) {
			return strings.TrimSpace(modelID) != unpricedModel, nil
		},
		ProjectDefaults: func(_ context.Context, _ string) (string, string, string, watch.Authority) {
			return "plan", "mock:gpt-test", "America/New_York", watch.Authority{}
		},
		SessionTargetCheck: func(c context.Context, projID, sessID string) (bool, bool, error) {
			sess, err := st.Sessions.Get(c, sessID)
			if err != nil || sess == nil || sess.ProjectID != projID {
				return false, false, nil
			}
			return true, false, nil
		},
		RunDispatcher: func(c context.Context, sw watch.StandingWatch, run watch.WatchRun, _ string) (watch.StandingRunDispatchOutcome, error) {
			callerCtx := people.WithCaller(c, people.Person{ID: sw.OwnerPersonID, Role: wire.PersonRoleOwner})
			sess, err := st.Sessions.Create(callerCtx, wire.CreateSessionRequest{
				ProjectID: sw.ProjectID,
				Posture:   wire.SessionPostureBuild,
			}, sw.ProjectID)
			if err != nil {
				return watch.StandingRunDispatchOutcome{}, err
			}
			_ = watchStore.Standing().RecordChatProvenance(c, watch.StandingChatProvenance{
				SessionID: sess.ID,
				ProjectID: sw.ProjectID,
				WatchID:   sw.WatchID,
				WatchName: sw.Name,
				RunID:     run.RunID,
			})
			return watch.StandingRunDispatchOutcome{SessionID: sess.ID}, nil
		},
	})
	watchStore.SetEventOutbox(outbox)

	reg := toolstest.NewRegistry()
	mock := llm.NewMockProvider(testMockConfig(t))
	mgr := session.NewHost(st, session.Models{
		Client: mock,
		Limits: settings.DefaultSessionLimits(),
	}, reg)
	mgr.SetDataDir(t.TempDir())
	mgr.SetProjectRegistry(projReg)
	mgr.Coordinator.Guards.SetToolMetadata(testtool.RegistryInvoker{Registry: reg})
	wireTestBindingRegistry(t)
	testutil.FailErr(t, "install anchor registry", mgr.Coordinator.Guidance.InstallAnchorRegistry())

	srv := newServerForTest(t, Dependencies{
		Core: CoreDependencies{
			Database:      sqlDB,
			Store:         st,
			Projects:      projReg,
			Sessions:      mgr,
			Watches:       watchStore,
			PersonActions: personactions.New(sqlDB),
		},
		Host: HostDependencies{
			Events:         hub,
			EventPublisher: publisher,
		},
	})
	owner, err := srv.peopleStore.HostOwner(t.Context())
	testutil.FailErr(t, "host owner", err)

	return &watchAPITestEnv{
		srv:         srv,
		watches:     watchStore,
		clock:       clock,
		hub:         hub,
		outbox:      outbox,
		store:       st,
		projects:    projReg,
		sessions:    mgr,
		owner:       owner,
		unpricedMdl: unpricedModel,
	}
}

func (env *watchAPITestEnv) doJSON(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		testutil.FailErr(t, "marshal request", err)
		buf = bytes.NewReader(raw)
	} else {
		buf = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Authorization", "Bearer "+TestAPIToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	return rec
}

func TestStandingWatchOperationsAndErrorCodes(t *testing.T) {
	env := newWatchAPITestEnv(t)
	ctx := people.WithCaller(t.Context(), env.owner)
	proj := createProjectForTest(t, env.srv, resolveTestPath(t, t.TempDir()))
	sess, err := env.store.Sessions.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, proj.ID)
	testutil.FailErr(t, "create session", err)

	// Unpriced model without allow_unpriced returns watch_spend_unenforceable.
	rec := env.doJSON(t, http.MethodPost, "/v1/projects/"+proj.ID+"/watches", wire.CreateStandingWatchRequest{
		Name:    "Unpriced check",
		Prompt:  "Review main",
		ModelID: env.unpricedMdl,
		Conditions: []wire.WatchCondition{{
			Kind:         "timer",
			EveryMinutes: 60,
		}},
	})
	assertErrorResponse(t, rec, http.StatusUnprocessableEntity, "watch_spend_unenforceable")

	// Person-owned process condition returns watch_condition_invalid.
	rec = env.doJSON(t, http.MethodPost, "/v1/projects/"+proj.ID+"/watches", wire.CreateStandingWatchRequest{
		Name:   "Invalid process watch",
		Prompt: "Check process",
		Conditions: []wire.WatchCondition{{
			Kind:      "process_done",
			ProcessID: "proc-1",
		}},
	})
	assertErrorResponse(t, rec, http.StatusUnprocessableEntity, "watch_condition_invalid")

	// Create standing watch (201).
	rec = env.doJSON(t, http.MethodPost, "/v1/projects/"+proj.ID+"/watches", wire.CreateStandingWatchRequest{
		Name:                 "Weekday commit review",
		Prompt:               "Summarize new commits on main.",
		RunSpendLimitNanoUsd: 2_000_000_000,
		DailyRunCap:          5,
		TimeZone:             "America/New_York",
		Conditions: []wire.WatchCondition{{
			Kind: "timer",
			Calendar: &wire.WatchCalendarSchedule{
				Days:  []string{"weekdays"},
				Times: []string{"09:00"},
			},
		}},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create standing watch status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created wire.StandingWatch
	testutil.FailErr(t, "decode created watch", json.Unmarshal(rec.Body.Bytes(), &created))
	if created.Revision != 1 || created.State != "enabled" || created.ConditionSummary != "Weekdays at 09:00 (America/New_York)" {
		t.Fatalf("unexpected created watch: %+v", created)
	}

	// Duplicate name returns watch_name_taken.
	rec = env.doJSON(t, http.MethodPost, "/v1/projects/"+proj.ID+"/watches", wire.CreateStandingWatchRequest{
		Name:   "weekday commit review",
		Prompt: "Duplicate",
		Conditions: []wire.WatchCondition{{
			Kind:         "timer",
			EveryMinutes: 30,
		}},
	})
	assertErrorResponse(t, rec, http.StatusConflict, "watch_name_taken")

	// Register an agent watch and verify listProjectWatches owner filters + daily spend cap.
	agentRec, err := env.watches.Register(ctx, watch.RegisterRequest{
		Owner:      watch.Owner{Kind: watch.OwnerSession, ProjectID: proj.ID, SessionID: sess.ID},
		Conditions: []awaitstore.Condition{{Kind: "timer", DelayMS: 120_000}},
	})
	testutil.FailErr(t, "register agent watch", err)

	rec = env.doJSON(t, http.MethodGet, "/v1/projects/"+proj.ID+"/watches", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list project watches status=%d body=%s", rec.Code, rec.Body.String())
	}
	var projList wire.ProjectWatchListResponse
	testutil.FailErr(t, "decode project watch list", json.Unmarshal(rec.Body.Bytes(), &projList))
	if len(projList.Watches) != 2 || projList.DailySpendCapNanoUsd != 10_000_000_000 || projList.EnabledStandingTotal != 1 {
		t.Fatalf("unexpected project watch list: %+v", projList)
	}

	// Addressing a standing-watch mutation to an agent watch returns watch_not_person_owned.
	rec = env.doJSON(t, http.MethodPost, "/v1/projects/"+proj.ID+"/watches/"+agentRec.WatchID+"/pause", wire.StandingWatchRevisionRequest{Revision: 1})
	assertErrorResponse(t, rec, http.StatusConflict, "watch_not_person_owned")

	// getProjectWatch works for both owners.
	rec = env.doJSON(t, http.MethodGet, "/v1/projects/"+proj.ID+"/watches/"+agentRec.WatchID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("getProjectWatch agent status=%d body=%s", rec.Code, rec.Body.String())
	}
	var gotAgentPW wire.ProjectWatch
	testutil.FailErr(t, "decode project agent watch", json.Unmarshal(rec.Body.Bytes(), &gotAgentPW))
	if gotAgentPW.SessionID != sess.ID || gotAgentPW.Handle != agentRec.Handle || gotAgentPW.AgentWatch == nil {
		t.Fatalf("unexpected project agent watch: %+v", gotAgentPW)
	}

	// Update standing watch with stale revision -> watch_revision_conflict, then valid revision.
	newCap := 2
	rec = env.doJSON(t, http.MethodPatch, "/v1/projects/"+proj.ID+"/watches/"+created.WatchID, wire.UpdateStandingWatchRequest{
		Revision:    99,
		DailyRunCap: &newCap,
	})
	assertErrorResponse(t, rec, http.StatusConflict, "watch_revision_conflict")

	rec = env.doJSON(t, http.MethodPatch, "/v1/projects/"+proj.ID+"/watches/"+created.WatchID, wire.UpdateStandingWatchRequest{
		Revision:    created.Revision,
		DailyRunCap: &newCap,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update standing watch status=%d body=%s", rec.Code, rec.Body.String())
	}
	var updated wire.StandingWatch
	testutil.FailErr(t, "decode updated watch", json.Unmarshal(rec.Body.Bytes(), &updated))
	if updated.DailyRunCap != 2 || updated.Revision != 2 {
		t.Fatalf("unexpected updated watch: %+v", updated)
	}

	// Pause and resume standing watch.
	rec = env.doJSON(t, http.MethodPost, "/v1/projects/"+proj.ID+"/watches/"+created.WatchID+"/pause", wire.StandingWatchRevisionRequest{Revision: updated.Revision})
	if rec.Code != http.StatusOK {
		t.Fatalf("pause status=%d body=%s", rec.Code, rec.Body.String())
	}
	var paused wire.StandingWatch
	testutil.FailErr(t, "decode paused watch", json.Unmarshal(rec.Body.Bytes(), &paused))
	if paused.State != "paused" {
		t.Fatalf("expected paused state, got %+v", paused)
	}

	rec = env.doJSON(t, http.MethodPost, "/v1/projects/"+proj.ID+"/watches/"+created.WatchID+"/resume", wire.StandingWatchRevisionRequest{Revision: paused.Revision})
	if rec.Code != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resumed wire.StandingWatch
	testutil.FailErr(t, "decode resumed watch", json.Unmarshal(rec.Body.Bytes(), &resumed))
	if resumed.State != "enabled" {
		t.Fatalf("expected enabled state, got %+v", resumed)
	}

	// Run now: first run starts and binds chat provenance; second run while active returns watch_run_active.
	rec = env.doJSON(t, http.MethodPost, "/v1/projects/"+proj.ID+"/watches/"+created.WatchID+"/run", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("run now status=%d body=%s", rec.Code, rec.Body.String())
	}
	var run1 wire.WatchRun
	testutil.FailErr(t, "decode run1", json.Unmarshal(rec.Body.Bytes(), &run1))
	if run1.Status != "running" || run1.SessionID == "" {
		t.Fatalf("unexpected run1: %+v", run1)
	}

	rec = env.doJSON(t, http.MethodPost, "/v1/projects/"+proj.ID+"/watches/"+created.WatchID+"/run", nil)
	assertErrorResponse(t, rec, http.StatusConflict, "watch_run_active")

	// Settle run1, run again to hit daily_run_cap=2, then verify watch_daily_cap_reached on third run.
	testutil.FailErr(t, "settle run1", env.watches.Standing().SettleRun(ctx, run1.SessionID, watch.RunCompleted))
	rec = env.doJSON(t, http.MethodPost, "/v1/projects/"+proj.ID+"/watches/"+created.WatchID+"/run", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("second run now status=%d body=%s", rec.Code, rec.Body.String())
	}
	var run2 wire.WatchRun
	testutil.FailErr(t, "decode run2", json.Unmarshal(rec.Body.Bytes(), &run2))
	testutil.FailErr(t, "settle run2", env.watches.Standing().SettleRun(ctx, run2.SessionID, watch.RunCompleted))

	rec = env.doJSON(t, http.MethodPost, "/v1/projects/"+proj.ID+"/watches/"+created.WatchID+"/run", nil)
	assertErrorResponse(t, rec, http.StatusConflict, "watch_daily_cap_reached")

	// listWatchRuns cursor pagination and invalid cursor refusal.
	rec = env.doJSON(t, http.MethodGet, "/v1/projects/"+proj.ID+"/watches/"+created.WatchID+"/runs?limit=1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("listWatchRuns page 1 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var page1 wire.WatchRunListResponse
	testutil.FailErr(t, "decode page1", json.Unmarshal(rec.Body.Bytes(), &page1))
	if len(page1.Runs) != 1 || page1.NextCursor == "" {
		t.Fatalf("expected 1 run and next_cursor, got %+v", page1)
	}
	rec = env.doJSON(t, http.MethodGet, "/v1/projects/"+proj.ID+"/watches/"+created.WatchID+"/runs?limit=1&cursor="+page1.NextCursor, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("listWatchRuns page 2 status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = env.doJSON(t, http.MethodGet, "/v1/projects/"+proj.ID+"/watches/"+created.WatchID+"/runs?limit=1&cursor=bad-cursor", nil)
	assertErrorResponse(t, rec, http.StatusBadRequest, "invalid_page_cursor")

	// Verify run chat provenance before and after deleteStandingWatch.
	rec = env.doJSON(t, http.MethodGet, "/v1/sessions/"+run1.SessionID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get run session status=%d body=%s", rec.Code, rec.Body.String())
	}
	var runSess wire.Session
	testutil.FailErr(t, "decode run session", json.Unmarshal(rec.Body.Bytes(), &runSess))
	if runSess.UI == nil || runSess.UI.StandingWatch == nil || runSess.UI.StandingWatch.Deleted {
		t.Fatalf("expected live standing watch provenance on session, got %+v", runSess.UI)
	}

	rec = env.doJSON(t, http.MethodDelete, "/v1/projects/"+proj.ID+"/watches/"+created.WatchID, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete standing watch status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = env.doJSON(t, http.MethodGet, "/v1/sessions/"+run1.SessionID, nil)
	var deletedRunSess wire.Session
	testutil.FailErr(t, "decode run session after delete", json.Unmarshal(rec.Body.Bytes(), &deletedRunSess))
	if deletedRunSess.UI == nil || deletedRunSess.UI.StandingWatch == nil || !deletedRunSess.UI.StandingWatch.Deleted || deletedRunSess.UI.StandingWatch.WatchName != "Weekday commit review" {
		t.Fatalf("expected deleted standing watch provenance on surviving chat, got %+v", deletedRunSess.UI)
	}
}

func TestWatchAuthorizationAndDisabledTakeover(t *testing.T) {
	env := newWatchAPITestEnv(t)
	ctx := people.WithCaller(t.Context(), env.owner)
	proj := createProjectForTest(t, env.srv, resolveTestPath(t, t.TempDir()))
	sess, err := env.store.Sessions.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, proj.ID)
	testutil.FailErr(t, "create session", err)

	// Missing/invalid bearer token returns 401 unauthorized.
	unauthReq := httptest.NewRequest(http.MethodGet, "/v1/projects/"+proj.ID+"/watches", nil)
	unauthRec := httptest.NewRecorder()
	env.srv.ServeHTTP(unauthRec, unauthReq)
	assertErrorResponse(t, unauthRec, http.StatusUnauthorized, "unauthorized")

	// Guest role fails authorizeOperation with 403 forbidden.
	guestCtx := people.WithCaller(t.Context(), people.Person{ID: uuid.NewString(), Role: wire.PersonRole("guest")})
	forbRec := httptest.NewRecorder()
	env.srv.authorizeOperation(operationListProjectWatches, env.srv.Routes.Watches.handleListProjectWatches)(
		forbRec, httptest.NewRequestWithContext(guestCtx, http.MethodGet, "/v1/projects/"+proj.ID+"/watches", nil))
	assertErrorResponse(t, forbRec, http.StatusForbidden, "forbidden")

	forbKeep := httptest.NewRecorder()
	env.srv.authorizeOperation(operationKeepWatch, env.srv.Routes.Watches.handleKeepWatch)(
		forbKeep, httptest.NewRequestWithContext(guestCtx, http.MethodPost, "/v1/sessions/"+sess.ID+"/watches/watch-1/keep", strings.NewReader("{}")))
	assertErrorResponse(t, forbKeep, http.StatusForbidden, "forbidden")

	// Non-owner members cannot edit, resume, or run now, yet may pause and take over when disabled.
	sw, err := env.watches.Standing().Create(ctx, env.owner.ID, watch.StandingWatchInput{
		ProjectID:  proj.ID,
		Name:       "Owner-guarded watch",
		Prompt:     "Check status",
		Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 30}},
	})
	testutil.FailErr(t, "create standing watch", err)

	otherMember := people.Person{ID: uuid.NewString(), Role: wire.PersonRoleOwner}
	otherCtx := people.WithCaller(t.Context(), otherMember)

	invoke := func(c context.Context, method, path string, body any) *httptest.ResponseRecorder {
		rctx := chiRouteRequest(c, method, path, body, map[string]string{"id": proj.ID, "watch_id": sw.WatchID})
		rec := httptest.NewRecorder()
		switch {
		case method == http.MethodPatch:
			env.srv.Routes.Watches.handleUpdateStandingWatch(rec, rctx)
		case strings.HasSuffix(path, "/pause"):
			env.srv.Routes.Watches.handlePauseStandingWatch(rec, rctx)
		case strings.HasSuffix(path, "/resume"):
			env.srv.Routes.Watches.handleResumeStandingWatch(rec, rctx)
		case strings.HasSuffix(path, "/run"):
			env.srv.Routes.Watches.handleRunStandingWatchNow(rec, rctx)
		}
		return rec
	}

	newPrompt := "Tampered prompt"
	assertErrorResponse(t, invoke(otherCtx, http.MethodPatch, "/v1/projects/"+proj.ID+"/watches/"+sw.WatchID, wire.UpdateStandingWatchRequest{
		Revision: sw.Revision, Prompt: &newPrompt,
	}), http.StatusForbidden, "watch_not_owner")

	assertErrorResponse(t, invoke(otherCtx, http.MethodPost, "/v1/projects/"+proj.ID+"/watches/"+sw.WatchID+"/run", nil),
		http.StatusForbidden, "watch_not_owner")

	// Another member CAN pause.
	pauseRec := invoke(otherCtx, http.MethodPost, "/v1/projects/"+proj.ID+"/watches/"+sw.WatchID+"/pause", wire.StandingWatchRevisionRequest{Revision: sw.Revision})
	if pauseRec.Code != http.StatusOK {
		t.Fatalf("member pause status=%d body=%s", pauseRec.Code, pauseRec.Body.String())
	}
	var paused wire.StandingWatch
	testutil.FailErr(t, "decode member pause", json.Unmarshal(pauseRec.Body.Bytes(), &paused))

	// Non-owner cannot resume.
	assertErrorResponse(t, invoke(otherCtx, http.MethodPost, "/v1/projects/"+proj.ID+"/watches/"+sw.WatchID+"/resume", wire.StandingWatchRevisionRequest{Revision: paused.Revision}),
		http.StatusForbidden, "watch_not_owner")

	// Mark watch disabled under a revoked former owner, then verify an active member can enable and become owner.
	formerOwner := uuid.NewString()
	_, _ = env.watches.Database().ExecContext(ctx, `PRAGMA foreign_keys = OFF`)
	_, err = env.watches.Database().ExecContext(ctx,
		`UPDATE watch_registrations SET owner_person_id = ?, state = 'disabled', state_reason = 'owner_access_revoked', revision = revision + 1 WHERE watch_id = ?`,
		formerOwner, sw.WatchID)
	_, _ = env.watches.Database().ExecContext(ctx, `PRAGMA foreign_keys = ON`)
	testutil.FailErr(t, "mark disabled", err)

	enableState := "enabled"
	takeoverRec := invoke(ctx, http.MethodPatch, "/v1/projects/"+proj.ID+"/watches/"+sw.WatchID, wire.UpdateStandingWatchRequest{
		Revision: paused.Revision + 1,
		State:    &enableState,
	})
	if takeoverRec.Code != http.StatusOK {
		t.Fatalf("disabled takeover status=%d body=%s", takeoverRec.Code, takeoverRec.Body.String())
	}
	var takenOver wire.StandingWatch
	testutil.FailErr(t, "decode takeover", json.Unmarshal(takeoverRec.Body.Bytes(), &takenOver))
	if takenOver.OwnerPersonID != env.owner.ID || takenOver.State != "enabled" {
		t.Fatalf("expected %s to become owner of enabled watch, got %+v", env.owner.ID, takenOver)
	}
}

func TestSessionWatchKeepCancelAndSSEReconnectReceipts(t *testing.T) {
	env := newWatchAPITestEnv(t)
	ctx := people.WithCaller(t.Context(), env.owner)
	baseURL := startTestHTTPServer(t, env.srv)
	proj := createProjectForTest(t, env.srv, resolveTestPath(t, t.TempDir()))
	sess, err := env.store.Sessions.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, proj.ID)
	testutil.FailErr(t, "create session", err)

	openSSE := func(after string) (*http.Response, *bufio.Reader, context.CancelFunc) {
		sctx, cancel := context.WithCancel(t.Context())
		url := baseURL + "/v1/events?project_id=" + proj.ID
		if after != "" {
			url += "&after=" + after
		}
		req, reqErr := http.NewRequestWithContext(sctx, http.MethodGet, url, nil)
		testutil.FailErr(t, "build SSE req", reqErr)
		req.Header.Set("Authorization", "Bearer "+TestAPIToken)
		resp, doErr := http.DefaultClient.Do(req)
		testutil.FailErr(t, "open SSE", doErr)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("SSE status=%d", resp.StatusCode)
		}
		return resp, bufio.NewReader(resp.Body), cancel
	}

	readWatchEnvelope := func(reader *bufio.Reader) (string, wire.WatchEvent) {
		t.Helper()
		for {
			line, readErr := reader.ReadString('\n')
			testutil.FailErr(t, "read SSE line", readErr)
			line = strings.TrimRight(line, "\r\n")
			data, ok := strings.CutPrefix(line, "data:")
			if !ok {
				continue
			}
			var envEvent struct {
				Cursor string           `json:"cursor"`
				Topic  wire.EventTopic  `json:"topic"`
				Data   json.RawMessage  `json:"data"`
			}
			testutil.FailErr(t, "decode SSE envelope", json.Unmarshal([]byte(strings.TrimSpace(data)), &envEvent))
			if envEvent.Topic == wire.EventTopicWatchUpdated {
				var ev wire.WatchEvent
				testutil.FailErr(t, "decode WatchEvent", json.Unmarshal(envEvent.Data, &ev))
				return envEvent.Cursor, ev
			}
		}
	}

	resp1, reader1, cancel1 := openSSE("")
	defer func() {
		cancel1()
		_ = resp1.Body.Close()
	}()

	// Register one plain timer watch, one draft watch, and one non-keepable process watch.
	plainRec, err := env.watches.Register(ctx, watch.RegisterRequest{
		Owner:      watch.Owner{Kind: watch.OwnerSession, ProjectID: proj.ID, SessionID: sess.ID},
		Conditions: []awaitstore.Condition{{Kind: "timer", DelayMS: 900_000}},
		Repeat:     true,
		Delivery:   watch.DeliveryCollect,
	})
	testutil.FailErr(t, "register plain watch", err)

	draftRec, err := env.watches.Register(ctx, watch.RegisterRequest{
		Owner:      watch.Owner{Kind: watch.OwnerSession, ProjectID: proj.ID, SessionID: sess.ID},
		Conditions: []awaitstore.Condition{{Kind: "timer", Calendar: &awaitstore.CalendarSpec{Days: []string{"weekdays"}, Times: []string{"09:00"}}}},
		Standing:   &watch.StandingDraft{Name: "Morning check", Prompt: "Check build health."},
	})
	testutil.FailErr(t, "register draft watch", err)

	procRec, err := env.watches.Register(ctx, watch.RegisterRequest{
		Owner:      watch.Owner{Kind: watch.OwnerSession, ProjectID: proj.ID, SessionID: sess.ID},
		Conditions: []awaitstore.Condition{{Kind: "process_done", Handles: []string{"proc-live"}}},
	})
	testutil.FailErr(t, "register process watch", err)

	var lastCursor string
	for range 3 {
		c, _ := readWatchEnvelope(reader1)
		lastCursor = c
	}

	// Switch delivery on plain one-shot watch: create one-shot git_ref watch and toggle collect -> wake.
	gitRec, err := env.watches.Register(ctx, watch.RegisterRequest{
		Owner:      watch.Owner{Kind: watch.OwnerSession, ProjectID: proj.ID, SessionID: sess.ID},
		Conditions: []awaitstore.Condition{{Kind: "http_ready", URL: "http://127.0.0.1:9999/health"}},
	})
	testutil.FailErr(t, "register http watch", err)
	lastCursor, _ = readWatchEnvelope(reader1)

	patchDeliv := env.doJSON(t, http.MethodPatch, "/v1/sessions/"+sess.ID+"/watches/"+gitRec.Handle, wire.UpdateWatchDeliveryRequest{
		Delivery: "wake",
	})
	if patchDeliv.Code != http.StatusOK {
		t.Fatalf("updateWatchDelivery status=%d body=%s", patchDeliv.Code, patchDeliv.Body.String())
	}
	var toggled wire.AgentWatch
	testutil.FailErr(t, "decode toggled watch", json.Unmarshal(patchDeliv.Body.Bytes(), &toggled))
	if toggled.Delivery != "wake" || toggled.Revision != 2 {
		t.Fatalf("unexpected toggled watch: %+v", toggled)
	}
	lastCursor, _ = readWatchEnvelope(reader1)

	// Non-keepable process watch returns watch_keep_unavailable.
	badKeep := env.doJSON(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/watches/"+procRec.Handle+"/keep", wire.KeepWatchRequest{
		Name:   "Bad keep",
		Prompt: "Cannot keep process",
	})
	assertErrorResponse(t, badKeep, http.StatusConflict, "watch_keep_unavailable")

	// Disconnect SSE stream before Cancel (Dismiss) and Keep, then reconnect with ?after=lastCursor.
	cancel1()
	_ = resp1.Body.Close()

	// Cancel draft watch -> dismissed final receipt.
	cancelDraft := env.doJSON(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/watches/"+draftRec.Handle+"/cancel", nil)
	if cancelDraft.Code != http.StatusOK {
		t.Fatalf("cancelWatch draft status=%d body=%s", cancelDraft.Code, cancelDraft.Body.String())
	}
	var cancelResp wire.CancelWatchResponse
	testutil.FailErr(t, "decode cancel response", json.Unmarshal(cancelDraft.Body.Bytes(), &cancelResp))
	if cancelResp.Receipt.TerminalOutcome != "dismissed" || cancelResp.Watch.State != "terminal" {
		t.Fatalf("expected dismissed outcome, got %+v", cancelResp)
	}

	// Keep plain repeating timer watch -> converts 900_000ms to every_minutes=15 and records kept final receipt.
	keepPlain := env.doJSON(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/watches/"+plainRec.Handle+"/keep", wire.KeepWatchRequest{
		Name:   "Every 15m status",
		Prompt: "Check CI every 15m.",
	})
	if keepPlain.Code != http.StatusOK {
		t.Fatalf("keepWatch status=%d body=%s", keepPlain.Code, keepPlain.Body.String())
	}
	var keepResp wire.KeepWatchResponse
	testutil.FailErr(t, "decode keep response", json.Unmarshal(keepPlain.Body.Bytes(), &keepResp))
	if keepResp.Receipt.TerminalOutcome != "kept" || keepResp.Receipt.StandingWatchID != keepResp.StandingWatch.WatchID {
		t.Fatalf("expected kept receipt naming standing watch, got %+v", keepResp)
	}
	if len(keepResp.StandingWatch.Conditions) != 1 || keepResp.StandingWatch.Conditions[0].EveryMinutes != 15 {
		t.Fatalf("expected delay_ms converted to every_minutes=15, got %+v", keepResp.StandingWatch.Conditions)
	}

	// Reconnect SSE with ?after=lastCursor and verify ordered replay of Dismiss + Keep + created StandingWatch!
	resp2, reader2, cancel2 := openSSE(lastCursor)
	defer func() {
		cancel2()
		_ = resp2.Body.Close()
	}()
	var replayed []wire.WatchEvent
	for range 3 {
		_, ev := readWatchEnvelope(reader2)
		replayed = append(replayed, ev)
	}
	if replayed[0].WatchID != draftRec.WatchID || replayed[0].Revision < 2 {
		t.Fatalf("unexpected first replayed event: %+v", replayed[0])
	}
	if replayed[1].WatchID != plainRec.WatchID || replayed[1].Revision < 2 {
		t.Fatalf("unexpected second replayed event: %+v", replayed[1])
	}
	if replayed[2].WatchID != keepResp.StandingWatch.WatchID || replayed[2].OwnerKind != "person" || replayed[2].Revision != 1 {
		t.Fatalf("unexpected third replayed event: %+v", replayed[2])
	}

	// Recover final receipts via GET /v1/sessions/{id}/watches/{handle} after reconnect.
	for _, tc := range []struct {
		handle      string
		wantOutcome string
		wantKeptID  string
	}{
		{handle: draftRec.Handle, wantOutcome: "dismissed"},
		{handle: plainRec.Handle, wantOutcome: "kept", wantKeptID: keepResp.StandingWatch.WatchID},
	} {
		getRec := env.doJSON(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/watches/"+tc.handle, nil)
		if getRec.Code != http.StatusOK {
			t.Fatalf("getWatch(%s) status=%d body=%s", tc.handle, getRec.Code, getRec.Body.String())
		}
		var aw wire.AgentWatch
		testutil.FailErr(t, "decode agent watch", json.Unmarshal(getRec.Body.Bytes(), &aw))
		if aw.FinalReceipt == nil || aw.FinalReceipt.TerminalOutcome != tc.wantOutcome || aw.FinalReceipt.StandingWatchID != tc.wantKeptID {
			t.Fatalf("recovered receipt for %s = %+v, want outcome=%s standing=%s", tc.handle, aw.FinalReceipt, tc.wantOutcome, tc.wantKeptID)
		}
	}

	// Also verify listWatches returns all 4 session watches.
	listSess := env.doJSON(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/watches", nil)
	if listSess.Code != http.StatusOK {
		t.Fatalf("listWatches status=%d body=%s", listSess.Code, listSess.Body.String())
	}
	var sessWatches wire.AgentWatchListResponse
	testutil.FailErr(t, "decode session watches", json.Unmarshal(listSess.Body.Bytes(), &sessWatches))
	if len(sessWatches.Watches) != 4 {
		t.Fatalf("expected 4 session watches, got %d", len(sessWatches.Watches))
	}
}

func chiRouteRequest(ctx context.Context, method, path string, body any, params map[string]string) *http.Request {
	var buf *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		buf = bytes.NewReader(raw)
	} else {
		buf = bytes.NewReader(nil)
	}
	req := httptest.NewRequestWithContext(ctx, method, path, buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}
