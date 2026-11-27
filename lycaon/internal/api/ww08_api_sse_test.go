package api

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/watch"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type ww08SSEEvent struct {
	Cursor string
	Topic  wire.EventTopic
	Raw    json.RawMessage
}

func readNextWW08Event(t *testing.T, reader *bufio.Reader, topics ...wire.EventTopic) ww08SSEEvent {
	t.Helper()
	want := make(map[wire.EventTopic]bool, len(topics))
	for _, tp := range topics {
		want[tp] = true
	}
	for {
		line, err := reader.ReadString('\n')
		testutil.FailErr(t, "read SSE line", err)
		line = strings.TrimRight(line, "\r\n")
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		var env struct {
			Cursor string          `json:"cursor"`
			Topic  wire.EventTopic `json:"topic"`
			Data   json.RawMessage `json:"data"`
		}
		testutil.FailErr(t, "decode SSE envelope", json.Unmarshal([]byte(strings.TrimSpace(data)), &env))
		if len(want) == 0 || want[env.Topic] {
			return ww08SSEEvent{Cursor: env.Cursor, Topic: env.Topic, Raw: env.Data}
		}
	}
}

// TestWW08_C28_ProjectSessionInventoryRevisionAndParity verifies C28 project/session
// HTTP inventory filtering, revision conflict enforcement (409 watch_revision_conflict),
// delivery switching, terminal history receipts, and exact parity with committed SQLite rows.
func TestWW08_C28_ProjectSessionInventoryRevisionAndParity(t *testing.T) {
	env := newWatchAPITestEnv(t)
	ctx := people.WithCaller(t.Context(), env.owner)
	proj := createProjectForTest(t, env.srv, resolveTestPath(t, t.TempDir()))
	sess, err := env.store.Sessions.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, proj.ID)
	testutil.FailErr(t, "create session", err)

	// Invalid owner query parameter must fail with 400 invalid_query.
	badQuery := env.doJSON(t, http.MethodGet, "/v1/projects/"+proj.ID+"/watches?owner=bogus", nil)
	assertErrorResponse(t, badQuery, http.StatusBadRequest, "invalid_query")

	// Create a person-owned standing watch.
	createRec := env.doJSON(t, http.MethodPost, "/v1/projects/"+proj.ID+"/watches", wire.CreateStandingWatchRequest{
		Name:                 "WW08 C28 API check",
		Prompt:               "Check main branch status.",
		RunSpendLimitNanoUsd: 1_500_000_000,
		DailyRunCap:          4,
		TimeZone:             "UTC",
		Conditions: []wire.WatchCondition{{
			Kind:         "timer",
			EveryMinutes: 60,
		}},
	})
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create standing watch status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var standing wire.StandingWatch
	testutil.FailErr(t, "decode standing watch", json.Unmarshal(createRec.Body.Bytes(), &standing))
	if standing.Revision != 1 || standing.State != "enabled" {
		t.Fatalf("unexpected created standing watch: %+v", standing)
	}

	// Register two sibling agent watches in the session.
	agentA, err := env.watches.Register(ctx, watch.RegisterRequest{
		Owner:      watch.Owner{Kind: watch.OwnerSession, ProjectID: proj.ID, SessionID: sess.ID},
		Conditions: []awaitstore.Condition{{Kind: "timer", DelayMS: 300_000}},
		Delivery:   watch.DeliveryCollect,
	})
	testutil.FailErr(t, "register agentA", err)
	agentB, err := env.watches.Register(ctx, watch.RegisterRequest{
		Owner:      watch.Owner{Kind: watch.OwnerSession, ProjectID: proj.ID, SessionID: sess.ID},
		Conditions: []awaitstore.Condition{{Kind: "timer", DelayMS: 600_000}},
		Delivery:   watch.DeliveryCollect,
	})
	testutil.FailErr(t, "register agentB", err)

	// Verify owner=person, owner=agent, and owner=all filters.
	for _, tc := range []struct {
		query     string
		wantCount int
		wantKind  string
	}{
		{query: "?owner=person", wantCount: 1, wantKind: "person"},
		{query: "?owner=agent", wantCount: 2, wantKind: "session"},
		{query: "?owner=all", wantCount: 3, wantKind: ""},
	} {
		rec := env.doJSON(t, http.MethodGet, "/v1/projects/"+proj.ID+"/watches"+tc.query, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list %s status=%d body=%s", tc.query, rec.Code, rec.Body.String())
		}
		var list wire.ProjectWatchListResponse
		testutil.FailErr(t, "decode list "+tc.query, json.Unmarshal(rec.Body.Bytes(), &list))
		if len(list.Watches) != tc.wantCount {
			t.Fatalf("list %s count=%d, want %d", tc.query, len(list.Watches), tc.wantCount)
		}
		if tc.wantKind != "" {
			for _, w := range list.Watches {
				if w.OwnerKind != tc.wantKind {
					t.Fatalf("list %s item owner_kind=%q, want %q", tc.query, w.OwnerKind, tc.wantKind)
				}
			}
		}
	}

	// Zero/missing revision on PATCH/pause/resume must return 400 invalid_request.
	newCap := 3
	assertErrorResponse(t, env.doJSON(t, http.MethodPatch, "/v1/projects/"+proj.ID+"/watches/"+standing.WatchID, wire.UpdateStandingWatchRequest{
		Revision:    0,
		DailyRunCap: &newCap,
	}), http.StatusBadRequest, "invalid_request")
	assertErrorResponse(t, env.doJSON(t, http.MethodPost, "/v1/projects/"+proj.ID+"/watches/"+standing.WatchID+"/pause", wire.StandingWatchRevisionRequest{
		Revision: 0,
	}), http.StatusBadRequest, "invalid_request")

	// Stale revision on PATCH and pause must return 409 watch_revision_conflict.
	assertErrorResponse(t, env.doJSON(t, http.MethodPatch, "/v1/projects/"+proj.ID+"/watches/"+standing.WatchID, wire.UpdateStandingWatchRequest{
		Revision:    42,
		DailyRunCap: &newCap,
	}), http.StatusConflict, "watch_revision_conflict")
	assertErrorResponse(t, env.doJSON(t, http.MethodPost, "/v1/projects/"+proj.ID+"/watches/"+standing.WatchID+"/pause", wire.StandingWatchRevisionRequest{
		Revision: 42,
	}), http.StatusConflict, "watch_revision_conflict")

	// Valid PATCH advances revision to 2; then reusing revision 1 fails with 409.
	patchOK := env.doJSON(t, http.MethodPatch, "/v1/projects/"+proj.ID+"/watches/"+standing.WatchID, wire.UpdateStandingWatchRequest{
		Revision:    1,
		DailyRunCap: &newCap,
	})
	if patchOK.Code != http.StatusOK {
		t.Fatalf("patch standing watch status=%d body=%s", patchOK.Code, patchOK.Body.String())
	}
	var updated wire.StandingWatch
	testutil.FailErr(t, "decode updated standing", json.Unmarshal(patchOK.Body.Bytes(), &updated))
	if updated.Revision != 2 || updated.DailyRunCap != 3 {
		t.Fatalf("unexpected updated standing: %+v", updated)
	}
	assertErrorResponse(t, env.doJSON(t, http.MethodPost, "/v1/projects/"+proj.ID+"/watches/"+standing.WatchID+"/pause", wire.StandingWatchRevisionRequest{
		Revision: 1,
	}), http.StatusConflict, "watch_revision_conflict")

	// Switch agentA delivery to wake and cancel agentA; verify sibling agentB remains live.
	delivRec := env.doJSON(t, http.MethodPatch, "/v1/sessions/"+sess.ID+"/watches/"+agentA.Handle, wire.UpdateWatchDeliveryRequest{
		Delivery: "wake",
	})
	if delivRec.Code != http.StatusOK {
		t.Fatalf("update delivery status=%d body=%s", delivRec.Code, delivRec.Body.String())
	}
	cancelRec := env.doJSON(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/watches/"+agentA.Handle+"/cancel", nil)
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("cancel agentA status=%d body=%s", cancelRec.Code, cancelRec.Body.String())
	}
	var cancelResp wire.CancelWatchResponse
	testutil.FailErr(t, "decode cancelResp", json.Unmarshal(cancelRec.Body.Bytes(), &cancelResp))
	if cancelResp.Watch.State != "terminal" || cancelResp.Receipt.Handle != agentA.Handle {
		t.Fatalf("unexpected cancel response: %+v", cancelResp)
	}

	// Verify sibling agentB is still active via HTTP and committed SQLite state matches HTTP parity.
	getB := env.doJSON(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/watches/"+agentB.Handle, nil)
	if getB.Code != http.StatusOK {
		t.Fatalf("get agentB status=%d body=%s", getB.Code, getB.Body.String())
	}
	var wireB wire.AgentWatch
	testutil.FailErr(t, "decode wireB", json.Unmarshal(getB.Body.Bytes(), &wireB))
	if wireB.State != "live" && wireB.State != "active" && wireB.State != "armed" {
		t.Fatalf("expected sibling agentB live/active, got state=%q", wireB.State)
	}

	// Direct SQLite parity check against watch_registrations.
	var dbStateA string
	var dbRevStanding int
	testutil.FailErr(t, "query agentA db state", env.watches.Database().QueryRowContext(
		ctx, `SELECT state FROM watch_registrations WHERE watch_id = ?`, agentA.WatchID,
	).Scan(&dbStateA))
	testutil.FailErr(t, "query standing db rev", env.watches.Database().QueryRowContext(
		ctx, `SELECT revision FROM watch_registrations WHERE watch_id = ?`, standing.WatchID,
	).Scan(&dbRevStanding))
	if dbStateA != "terminal" {
		t.Fatalf("committed SQLite state for canceled agentA = %q, want terminal", dbStateA)
	}
	if dbRevStanding != updated.Revision {
		t.Fatalf("committed SQLite revision for standing watch = %d, want %d", dbRevStanding, updated.Revision)
	}
}

// TestWW08_C28_MultiClientSSEReconnectAndDedup verifies multiple concurrent SSE clients,
// ordered delivery of watch_updated events, cursor-based reconnect replay, and zero duplicates.
func TestWW08_C28_MultiClientSSEReconnectAndDedup(t *testing.T) {
	env := newWatchAPITestEnv(t)
	ctx := people.WithCaller(t.Context(), env.owner)
	baseURL := startTestHTTPServer(t, env.srv)
	proj := createProjectForTest(t, env.srv, resolveTestPath(t, t.TempDir()))
	sess, err := env.store.Sessions.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, proj.ID)
	testutil.FailErr(t, "create session", err)

	// Open two concurrent SSE clients on the same project.
	respA, readerA, cancelA := openProjectWatchSSE(t, baseURL, proj.ID, "")
	defer func() {
		cancelA()
		_ = respA.Body.Close()
	}()
	respB, readerB, cancelB := openProjectWatchSSE(t, baseURL, proj.ID, "")
	defer func() {
		cancelB()
		_ = respB.Body.Close()
	}()

	// Mutation 1: register agent watch.
	reg, err := env.watches.Register(ctx, watch.RegisterRequest{
		Owner:      watch.Owner{Kind: watch.OwnerSession, ProjectID: proj.ID, SessionID: sess.ID},
		Conditions: []awaitstore.Condition{{Kind: "timer", DelayMS: 300_000}},
		Delivery:   watch.DeliveryCollect,
	})
	testutil.FailErr(t, "register agent watch", err)

	curA1, evA1 := readNextWatchUpdatedEnvelope(t, readerA)
	curB1, evB1 := readNextWatchUpdatedEnvelope(t, readerB)
	if evA1.WatchID != reg.WatchID || evB1.WatchID != reg.WatchID || curA1 != curB1 {
		t.Fatalf("multi-client initial event mismatch: A=(%s,%+v) B=(%s,%+v)", curA1, evA1, curB1, evB1)
	}

	// Disconnect clientA at curA1; perform two mutations while clientA is offline.
	cancelA()
	_ = respA.Body.Close()

	delivRec := env.doJSON(t, http.MethodPatch, "/v1/sessions/"+sess.ID+"/watches/"+reg.Handle, wire.UpdateWatchDeliveryRequest{
		Delivery: "wake",
	})
	if delivRec.Code != http.StatusOK {
		t.Fatalf("patch delivery status=%d body=%s", delivRec.Code, delivRec.Body.String())
	}
	cancelWatchRec := env.doJSON(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/watches/"+reg.Handle+"/cancel", nil)
	if cancelWatchRec.Code != http.StatusOK {
		t.Fatalf("cancel watch status=%d body=%s", cancelWatchRec.Code, cancelWatchRec.Body.String())
	}

	// ClientB stays live and receives both mutations.
	curB2, evB2 := readNextWatchUpdatedEnvelope(t, readerB)
	curB3, evB3 := readNextWatchUpdatedEnvelope(t, readerB)
	if evB2.WatchID != reg.WatchID || evB3.WatchID != reg.WatchID || evB3.Revision <= evB2.Revision {
		t.Fatalf("clientB live sequence invalid: ev2=%+v ev3=%+v", evB2, evB3)
	}

	// Reconnect clientA from curA1 and verify exact replay with zero duplicates.
	respA2, readerA2, cancelA2 := openProjectWatchSSE(t, baseURL, proj.ID, curA1)
	defer func() {
		cancelA2()
		_ = respA2.Body.Close()
	}()
	curA2, evA2 := readNextWatchUpdatedEnvelope(t, readerA2)
	curA3, evA3 := readNextWatchUpdatedEnvelope(t, readerA2)
	if curA2 != curB2 || curA3 != curB3 {
		t.Fatalf("reconnect cursor parity mismatch: A=(%s,%s) B=(%s,%s)", curA2, curA3, curB2, curB3)
	}
	if evA2.Revision != evB2.Revision || evA3.Revision != evB3.Revision {
		t.Fatalf("reconnect revision parity mismatch: A=(%d,%d) B=(%d,%d)", evA2.Revision, evA3.Revision, evB2.Revision, evB3.Revision)
	}
	if curA2 == curA1 || curA3 == curA2 {
		t.Fatalf("duplicate cursor observed across reconnect: %s -> %s -> %s", curA1, curA2, curA3)
	}

	// Verify terminal receipt recovered via HTTP after SSE reconnect matches committed record.
	getRec := env.doJSON(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/watches/"+reg.Handle, nil)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get terminal watch status=%d body=%s", getRec.Code, getRec.Body.String())
	}
	var aw wire.AgentWatch
	testutil.FailErr(t, "decode terminal watch", json.Unmarshal(getRec.Body.Bytes(), &aw))
	if aw.State != "terminal" || aw.FinalReceipt == nil || aw.FinalReceipt.Handle != reg.Handle {
		t.Fatalf("terminal watch parity failed after reconnect: %+v", aw)
	}
}

// TestWW08_C28_AccessScopeAndCrossProjectIsolation verifies auth, role enforcement,
// and strict cross-project / cross-session isolation on watch endpoints.
func TestWW08_C28_AccessScopeAndCrossProjectIsolation(t *testing.T) {
	env := newWatchAPITestEnv(t)
	ctx := people.WithCaller(t.Context(), env.owner)
	projA := createProjectForTest(t, env.srv, resolveTestPath(t, t.TempDir()))
	projB := createProjectForTest(t, env.srv, resolveTestPath(t, t.TempDir()))
	sessA, err := env.store.Sessions.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, projA.ID)
	testutil.FailErr(t, "create sessA", err)
	sessB, err := env.store.Sessions.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, projB.ID)
	testutil.FailErr(t, "create sessB", err)

	// Unauthenticated SSE and watch endpoints return 401 unauthorized.
	for _, path := range []string{
		"/v1/events?project_id=" + projA.ID,
		"/v1/projects/" + projA.ID + "/watches",
		"/v1/sessions/" + sessA.ID + "/watches",
	} {
		rec := httptest.NewRecorder()
		env.srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assertErrorResponse(t, rec, http.StatusUnauthorized, "unauthorized")
	}

	// Create standing watch in projA and agent watch in sessA.
	sw, err := env.watches.Standing().Create(ctx, env.owner.ID, watch.StandingWatchInput{
		ProjectID:  projA.ID,
		Name:       "Scoped watch A",
		Prompt:     "Run scoped check.",
		Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 30}},
	})
	testutil.FailErr(t, "create standing watch in projA", err)
	aw, err := env.watches.Register(ctx, watch.RegisterRequest{
		Owner:      watch.Owner{Kind: watch.OwnerSession, ProjectID: projA.ID, SessionID: sessA.ID},
		Conditions: []awaitstore.Condition{{Kind: "timer", DelayMS: 120_000}},
	})
	testutil.FailErr(t, "register agent watch in sessA", err)

	// Cross-project lookup and delete on projB must return 404 watch_not_found.
	assertErrorResponse(t, env.doJSON(t, http.MethodGet, "/v1/projects/"+projB.ID+"/watches/"+sw.WatchID, nil),
		http.StatusNotFound, "watch_not_found")
	assertErrorResponse(t, env.doJSON(t, http.MethodDelete, "/v1/projects/"+projB.ID+"/watches/"+sw.WatchID, nil),
		http.StatusNotFound, "watch_not_found")

	// projB watch inventory must be empty.
	listB := env.doJSON(t, http.MethodGet, "/v1/projects/"+projB.ID+"/watches", nil)
	if listB.Code != http.StatusOK {
		t.Fatalf("list projB watches status=%d body=%s", listB.Code, listB.Body.String())
	}
	var respB wire.ProjectWatchListResponse
	testutil.FailErr(t, "decode projB list", json.Unmarshal(listB.Body.Bytes(), &respB))
	if len(respB.Watches) != 0 {
		t.Fatalf("projB leaked watches from projA: %+v", respB.Watches)
	}

	// Cross-session lookup on sessB for sessA's handle must return 404 watch_not_found.
	assertErrorResponse(t, env.doJSON(t, http.MethodGet, "/v1/sessions/"+sessB.ID+"/watches/"+aw.Handle, nil),
		http.StatusNotFound, "watch_not_found")

	// Guest caller cannot cancel or mutate session watches.
	guestCtx := people.WithCaller(t.Context(), people.Person{ID: uuid.NewString(), Role: wire.PersonRole("guest")})
	guestCancel := httptest.NewRecorder()
	env.srv.authorizeOperation(operationCancelWatch, env.srv.Routes.Watches.handleCancelWatch)(
		guestCancel,
		chiRouteRequest(guestCtx, http.MethodPost, "/v1/sessions/"+sessA.ID+"/watches/"+aw.Handle+"/cancel", nil,
			map[string]string{"id": sessA.ID, "watch_handle": aw.Handle}),
	)
	assertErrorResponse(t, guestCancel, http.StatusForbidden, "forbidden")
}

// TestWW08_C28_PR2717ReconciliationParity verifies that the #2717 reconciled wire/event
// types (CredentialIndexingProgress / ActivityKindIndexingCredentials) coexist cleanly
// with watch_updated events on the same /v1/events SSE stream and wire schema.
func TestWW08_C28_PR2717ReconciliationParity(t *testing.T) {
	env := newWatchAPITestEnv(t)
	ctx := people.WithCaller(t.Context(), env.owner)
	baseURL := startTestHTTPServer(t, env.srv)
	proj := createProjectForTest(t, env.srv, resolveTestPath(t, t.TempDir()))
	sess, err := env.store.Sessions.Create(ctx, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, proj.ID)
	testutil.FailErr(t, "create session", err)

	// Verify #2717 wire type round-trip alongside watch wire types.
	credProg := wire.CredentialIndexingProgress{
		RootID:              "root-1",
		RootLabel:           "repo",
		DirectoriesVisited:  12,
		CredentialFilesRead: 2,
	}
	rawCred, err := json.Marshal(credProg)
	testutil.FailErr(t, "marshal CredentialIndexingProgress", err)
	var decodedCred wire.CredentialIndexingProgress
	testutil.FailErr(t, "unmarshal CredentialIndexingProgress", json.Unmarshal(rawCred, &decodedCred))
	if decodedCred != credProg || wire.ActivityKindIndexingCredentials != "indexing_credentials" {
		t.Fatalf("PR #2717 wire parity mismatch: decoded=%+v kind=%q", decodedCred, wire.ActivityKindIndexingCredentials)
	}

	// Open SSE stream on project and emit both an indexing_credentials activity event and a watch_updated event.
	resp, reader, cancel := openProjectWatchSSE(t, baseURL, proj.ID, "")
	defer func() {
		cancel()
		_ = resp.Body.Close()
	}()

	pub := &events.Publisher{Hub: env.hub}
	pub.PublishActivity(ctx, proj.ID, sess.ID, wire.ActivityEvent{
		ActivityID: uuid.NewString(),
		SessionID:  sess.ID,
		Kind:       wire.ActivityKindIndexingCredentials,
		Status:     wire.ActivityStatusActive,
		StartedAt:  env.clock.Now().UTC(),
		Indexing: &wire.CredentialIndexingProgress{
			RootID:              "root-1",
			RootLabel:           "repo",
			DirectoriesVisited:  5,
			CredentialFilesRead: 1,
		},
	})

	reg, err := env.watches.Register(ctx, watch.RegisterRequest{
		Owner:      watch.Owner{Kind: watch.OwnerSession, ProjectID: proj.ID, SessionID: sess.ID},
		Conditions: []awaitstore.Condition{{Kind: "timer", DelayMS: 60_000}},
	})
	testutil.FailErr(t, "register watch after activity event", err)

	ev1 := readNextWW08Event(t, reader, wire.EventTopicActivity, wire.EventTopicWatchUpdated)
	ev2 := readNextWW08Event(t, reader, wire.EventTopicActivity, wire.EventTopicWatchUpdated)

	seenActivity := false
	seenWatch := false
	for _, ev := range []ww08SSEEvent{ev1, ev2} {
		switch ev.Topic {
		case wire.EventTopicActivity:
			var act wire.ActivityEvent
			testutil.FailErr(t, "decode ActivityEvent", json.Unmarshal(ev.Raw, &act))
			if act.Kind == wire.ActivityKindIndexingCredentials && act.Indexing != nil && act.Indexing.RootID == "root-1" {
				seenActivity = true
			}
		case wire.EventTopicWatchUpdated:
			var we wire.WatchEvent
			testutil.FailErr(t, "decode WatchEvent", json.Unmarshal(ev.Raw, &we))
			if we.WatchID == reg.WatchID {
				seenWatch = true
			}
		}
	}
	if !seenActivity || !seenWatch {
		t.Fatalf("expected both indexing_credentials activity and watch_updated on shared SSE stream (activity=%v, watch=%v)", seenActivity, seenWatch)
	}
	if ev1.Cursor == ev2.Cursor {
		t.Fatalf("shared SSE stream emitted colliding cursor %q across activity and watch_updated", ev1.Cursor)
	}
}
