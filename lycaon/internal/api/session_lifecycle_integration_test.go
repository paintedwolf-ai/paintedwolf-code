//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func createLifecycleFixture(t *testing.T, srv *Server, titles []string) (string, []wire.Session) {
	t.Helper()
	createProj := newAuthedRequest(http.MethodPost, "/v1/projects", strings.NewReader(`{"roots":[]}`))
	projRec := httptest.NewRecorder()
	srv.ServeHTTP(projRec, createProj)
	if projRec.Code != http.StatusCreated {
		t.Fatalf("create project status = %d body = %s", projRec.Code, projRec.Body.String())
	}
	var proj wire.Project
	if err := json.Unmarshal(projRec.Body.Bytes(), &proj); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	sessions := make([]wire.Session, 0, len(titles))
	for _, title := range titles {
		createSess := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(
			`{"project_id":"`+proj.ID+`","posture":"build"}`,
		))
		sessRec := httptest.NewRecorder()
		srv.ServeHTTP(sessRec, createSess)
		if sessRec.Code != http.StatusAccepted {
			t.Fatalf("create session status = %d body = %s", sessRec.Code, sessRec.Body.String())
		}
		var sess wire.Session
		if err := json.Unmarshal(sessRec.Body.Bytes(), &sess); err != nil {
			t.Fatalf("decode session: %v", err)
		}
		drainBackground(t, srv)
		patch := newAuthedRequest(http.MethodPatch, "/v1/sessions/"+sess.ID, strings.NewReader(
			fmt.Sprintf(`{"title":%q}`, title),
		))
		patchRec := httptest.NewRecorder()
		srv.ServeHTTP(patchRec, patch)
		if patchRec.Code != http.StatusOK {
			t.Fatalf("title patch status = %d body = %s", patchRec.Code, patchRec.Body.String())
		}
		sessions = append(sessions, sess)
	}
	return proj.ID, sessions
}

func listSessionsPage(t *testing.T, srv *Server, projectID, query string) wire.SessionListPage {
	t.Helper()
	req := newAuthedRequest(http.MethodGet, "/v1/projects/"+projectID+"/sessions"+query, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", rec.Code, rec.Body.String())
	}
	var page wire.SessionListPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	return page
}

func TestListProjectSessionsFilterSortPage(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	srv := newTestServer(t)
	projectID, _ := createLifecycleFixture(t, srv, []string{"alpha plan", "beta fix", "gamma audit"})

	page := listSessionsPage(t, srv, projectID, "")
	if page.Total != 3 || len(page.Sessions) != 3 {
		t.Fatalf("default list total = %d rows = %d", page.Total, len(page.Sessions))
	}
	// Renames are not activity, so the newest chat leads.
	if page.Sessions[0].Title != "gamma audit" {
		t.Fatalf("expected activity order, first = %q", page.Sessions[0].Title)
	}
	if page.Sessions[0].ArchivedAt != nil || page.Sessions[0].PinRank != nil {
		t.Fatal("fresh session must be neither archived nor pinned")
	}
	createdAsc := listSessionsPage(t, srv, projectID, "?sort=created&order=asc")
	if createdAsc.Sessions[0].Title != "alpha plan" {
		t.Fatalf("created sort first = %q", createdAsc.Sessions[0].Title)
	}

	titleAsc := listSessionsPage(t, srv, projectID, "?sort=title")
	if titleAsc.Sessions[0].Title != "alpha plan" {
		t.Fatalf("title sort first = %q", titleAsc.Sessions[0].Title)
	}

	filtered := listSessionsPage(t, srv, projectID, "?q=BETA")
	if filtered.Total != 1 || filtered.Sessions[0].Title != "beta fix" {
		t.Fatalf("title filter got total=%d", filtered.Total)
	}

	first := listSessionsPage(t, srv, projectID, "?sort=title&limit=2")
	if first.NextCursor == "" {
		t.Fatal("first page has no next cursor")
	}
	paged := listSessionsPage(t, srv, projectID,
		"?sort=title&limit=2&cursor="+url.QueryEscape(first.NextCursor))
	if paged.Total != 3 || len(paged.Sessions) != 1 || paged.Sessions[0].Title != "gamma audit" {
		t.Fatalf("paging total=%d rows=%d", paged.Total, len(paged.Sessions))
	}

	bad := newAuthedRequest(http.MethodGet, "/v1/projects/"+projectID+"/sessions?sort=bogus", nil)
	badRec := httptest.NewRecorder()
	srv.ServeHTTP(badRec, bad)
	assertErrorResponse(t, badRec, http.StatusBadRequest, "invalid_query")
}

func TestSessionArchivePinLifecycle(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	srv := newTestServer(t)
	projectID, sessions := createLifecycleFixture(t, srv, []string{"keep", "shelve"})

	archive := newAuthedRequest(http.MethodPatch, "/v1/sessions/"+sessions[1].ID, strings.NewReader(`{"archived":true}`))
	archiveRec := httptest.NewRecorder()
	srv.ServeHTTP(archiveRec, archive)
	if archiveRec.Code != http.StatusOK {
		t.Fatalf("archive status = %d body = %s", archiveRec.Code, archiveRec.Body.String())
	}
	var archived wire.Session
	if err := json.Unmarshal(archiveRec.Body.Bytes(), &archived); err != nil {
		t.Fatalf("decode archived: %v", err)
	}
	if archived.ArchivedAt == nil {
		t.Fatal("archived_at not stamped")
	}

	active := listSessionsPage(t, srv, projectID, "")
	if active.Total != 1 || active.Sessions[0].Title != "keep" {
		t.Fatalf("active list total = %d", active.Total)
	}
	shelved := listSessionsPage(t, srv, projectID, "?archived=true")
	if shelved.Total != 1 || shelved.Sessions[0].Title != "shelve" {
		t.Fatalf("archived list total = %d", shelved.Total)
	}

	unarchive := newAuthedRequest(http.MethodPatch, "/v1/sessions/"+sessions[1].ID, strings.NewReader(`{"archived":false}`))
	unarchiveRec := httptest.NewRecorder()
	srv.ServeHTTP(unarchiveRec, unarchive)
	if unarchiveRec.Code != http.StatusOK {
		t.Fatalf("unarchive status = %d", unarchiveRec.Code)
	}
	if listSessionsPage(t, srv, projectID, "").Total != 2 {
		t.Fatal("unarchive did not restore the active list")
	}

	pin := newAuthedRequest(http.MethodPatch, "/v1/sessions/"+sessions[0].ID, strings.NewReader(`{"pinned":true}`))
	pinRec := httptest.NewRecorder()
	srv.ServeHTTP(pinRec, pin)
	if pinRec.Code != http.StatusOK {
		t.Fatalf("pin status = %d", pinRec.Code)
	}
	var pinned wire.Session
	if err := json.Unmarshal(pinRec.Body.Bytes(), &pinned); err != nil {
		t.Fatalf("decode pinned: %v", err)
	}
	if pinned.PinRank == nil || *pinned.PinRank != 1 {
		t.Fatalf("pin_rank = %v, want 1", pinned.PinRank)
	}

	noField := newAuthedRequest(http.MethodPatch, "/v1/sessions/"+sessions[0].ID, strings.NewReader(`{}`))
	noFieldRec := httptest.NewRecorder()
	srv.ServeHTTP(noFieldRec, noField)
	assertErrorResponse(t, noFieldRec, http.StatusBadRequest, "invalid_request")
}

func patchSession(t *testing.T, srv *Server, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, newAuthedRequest(http.MethodPatch, "/v1/sessions/"+id, strings.NewReader(body)))
	return rec
}

func pinnedTitles(t *testing.T, srv *Server, projectID string) []string {
	t.Helper()
	page := listSessionsPage(t, srv, projectID, "?pinned=true&sort=pin")
	titles := make([]string, 0, len(page.Sessions))
	for _, row := range page.Sessions {
		titles = append(titles, row.Title)
	}
	return titles
}

func TestSessionPinOrder(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	srv := newTestServer(t)
	projectID, sessions := createLifecycleFixture(t, srv, []string{"first", "second", "third", "loose"})
	for _, sess := range sessions[:3] {
		if rec := patchSession(t, srv, sess.ID, `{"pinned":true}`); rec.Code != http.StatusOK {
			t.Fatalf("pin status = %d body = %s", rec.Code, rec.Body.String())
		}
	}
	if got := strings.Join(pinnedTitles(t, srv, projectID), ","); got != "first,second,third" {
		t.Fatalf("pins append in pin order, got %s", got)
	}
	if rec := patchSession(t, srv, sessions[0].ID, `{"pinned":true}`); rec.Code != http.StatusOK {
		t.Fatalf("repeat pin status = %d", rec.Code)
	}
	if got := strings.Join(pinnedTitles(t, srv, projectID), ","); got != "first,second,third" {
		t.Fatalf("repeat pin moved the chat: %s", got)
	}

	rec := patchSession(t, srv, sessions[2].ID, `{"pin_position":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("move status = %d body = %s", rec.Code, rec.Body.String())
	}
	var moved wire.Session
	testutil.FailErr(t, "decode moved", json.Unmarshal(rec.Body.Bytes(), &moved))
	if moved.PinRank == nil || *moved.PinRank != 1 {
		t.Fatalf("moved pin_rank = %v, want 1", moved.PinRank)
	}
	if got := strings.Join(pinnedTitles(t, srv, projectID), ","); got != "third,first,second" {
		t.Fatalf("move to top, got %s", got)
	}
	if rec := patchSession(t, srv, sessions[2].ID, `{"pin_position":99}`); rec.Code != http.StatusOK {
		t.Fatalf("move past end status = %d", rec.Code)
	}
	if got := strings.Join(pinnedTitles(t, srv, projectID), ","); got != "first,second,third" {
		t.Fatalf("move past the end places last, got %s", got)
	}

	unpinned := listSessionsPage(t, srv, projectID, "?pinned=false")
	if unpinned.Total != 1 || unpinned.Sessions[0].Title != "loose" {
		t.Fatalf("pinned=false total = %d", unpinned.Total)
	}

	if rec := patchSession(t, srv, sessions[1].ID, `{"archived":true}`); rec.Code != http.StatusOK {
		t.Fatalf("archive status = %d", rec.Code)
	}
	if got := strings.Join(pinnedTitles(t, srv, projectID), ","); got != "first,third" {
		t.Fatalf("archiving unpins, got %s", got)
	}
	if rec := patchSession(t, srv, sessions[1].ID, `{"archived":false}`); rec.Code != http.StatusOK {
		t.Fatalf("unarchive status = %d", rec.Code)
	}
	if got := strings.Join(pinnedTitles(t, srv, projectID), ","); got != "first,third" {
		t.Fatalf("unarchiving does not restore a pin, got %s", got)
	}
	if rec := patchSession(t, srv, sessions[1].ID, `{"pinned":true}`); rec.Code != http.StatusOK {
		t.Fatalf("re-pin status = %d", rec.Code)
	}
	if got := strings.Join(pinnedTitles(t, srv, projectID), ","); got != "first,third,second" {
		t.Fatalf("re-pin appends, got %s", got)
	}

	if rec := patchSession(t, srv, sessions[0].ID, `{"pinned":false}`); rec.Code != http.StatusOK {
		t.Fatalf("unpin status = %d", rec.Code)
	}
	if got := strings.Join(pinnedTitles(t, srv, projectID), ","); got != "third,second" {
		t.Fatalf("unpin, got %s", got)
	}

	assertErrorResponse(t, patchSession(t, srv, sessions[3].ID, `{"pin_position":1}`), http.StatusConflict, "session_not_pinned")
	assertErrorResponse(t, patchSession(t, srv, sessions[2].ID, `{"pin_position":0}`), http.StatusBadRequest, "invalid_request")
	assertErrorResponse(t, patchSession(t, srv, sessions[2].ID, `{"pinned":true,"pin_position":1}`), http.StatusBadRequest, "invalid_request")
	if rec := patchSession(t, srv, sessions[3].ID, `{"archived":true}`); rec.Code != http.StatusOK {
		t.Fatalf("archive loose status = %d", rec.Code)
	}
	assertErrorResponse(t, patchSession(t, srv, sessions[3].ID, `{"pinned":true}`), http.StatusConflict, "session_archived")

	bad := httptest.NewRecorder()
	srv.ServeHTTP(bad, newAuthedRequest(http.MethodGet, "/v1/projects/"+projectID+"/sessions?sort=pin", nil))
	assertErrorResponse(t, bad, http.StatusBadRequest, "invalid_query")
}

func TestSessionRecordChangesAreNotActivity(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	srv := newTestServer(t)
	projectID, sessions := createLifecycleFixture(t, srv, []string{"older", "newer"})
	before := listSessionsPage(t, srv, projectID, "")

	seen := httptest.NewRecorder()
	srv.ServeHTTP(seen, newAuthedRequest(http.MethodPost, "/v1/sessions/"+sessions[0].ID+"/seen", nil))
	if seen.Code != http.StatusOK {
		t.Fatalf("seen status = %d body = %s", seen.Code, seen.Body.String())
	}
	for _, body := range []string{`{"title":"older renamed"}`, `{"pinned":true}`, `{"pinned":false}`} {
		if rec := patchSession(t, srv, sessions[0].ID, body); rec.Code != http.StatusOK {
			t.Fatalf("patch %s status = %d", body, rec.Code)
		}
	}

	after := listSessionsPage(t, srv, projectID, "")
	if after.Sessions[0].ID != sessions[1].ID {
		t.Fatalf("reading, renaming, and pinning moved the older chat to the top")
	}
	for i := range before.Sessions {
		if !after.Sessions[i].ActivityAt.Equal(before.Sessions[i].ActivityAt) {
			t.Fatalf("activity_at of %s moved from %s to %s", after.Sessions[i].ID,
				before.Sessions[i].ActivityAt, after.Sessions[i].ActivityAt)
		}
	}
}

func TestDeleteSessionRemovesRow(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	srv := newTestServer(t)
	projectID, sessions := createLifecycleFixture(t, srv, []string{"doomed", "survivor"})

	del := newAuthedRequest(http.MethodDelete, "/v1/sessions/"+sessions[0].ID, nil)
	delRec := httptest.NewRecorder()
	srv.ServeHTTP(delRec, del)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body = %s", delRec.Code, delRec.Body.String())
	}

	get := newAuthedRequest(http.MethodGet, "/v1/sessions/"+sessions[0].ID, nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, get)
	assertErrorResponse(t, getRec, http.StatusNotFound, "session_not_found")

	page := listSessionsPage(t, srv, projectID, "")
	if page.Total != 1 || page.Sessions[0].Title != "survivor" {
		t.Fatalf("post-delete list total = %d", page.Total)
	}

	again := newAuthedRequest(http.MethodDelete, "/v1/sessions/"+sessions[0].ID, nil)
	againRec := httptest.NewRecorder()
	srv.ServeHTTP(againRec, again)
	assertErrorResponse(t, againRec, http.StatusNotFound, "session_not_found")
}

func TestSessionCreateDeletePublishLifecycleActions(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	ctx := context.Background()
	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{})
	testutil.FailErr(t, "create project", err)
	mem := store.NewMemory()
	hub := events.NewMemoryHub()
	mgr := session.NewManager(mem, nil, nil, settings.DefaultSessionLimits())
	mgr.SetProjectRegistry(reg)
	mgr.SetEventPublisher(&events.Publisher{
		Hub:    hub,
		Lookup: project.ScopeLookup{Registry: reg},
	})
	srv := NewServer(requiredTestDeps(t, Dependencies{Store: mem, Projects: reg, Sessions: mgr, Events: hub}), nil, TestAPIToken)
	eventsCh, unsubscribe, err := hub.Subscribe(ctx, events.Subscription{Project: p.ID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe events", err)
	defer unsubscribe()

	createReq := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(
		`{"project_id":"`+p.ID+`","posture":"spec"}`,
	))
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create session status = %d body = %s", createRec.Code, createRec.Body.String())
	}
	var created wire.Session
	testutil.FailErr(t, "decode created session", json.Unmarshal(createRec.Body.Bytes(), &created))
	hub.FlushDebounced()
	assertSessionLifecycleEvent(t, eventsCh, created.ID, p.ID, wire.SessionEventActionCreated)
	drainBackground(t, srv)
	hub.FlushDebounced()
	assertSessionLifecycleEvent(t, eventsCh, created.ID, p.ID, wire.SessionEventActionUpdated)

	deleteReq := newAuthedRequest(http.MethodDelete, "/v1/sessions/"+created.ID, nil)
	deleteRec := httptest.NewRecorder()
	srv.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete session status = %d body = %s", deleteRec.Code, deleteRec.Body.String())
	}
	hub.FlushDebounced()
	assertSessionLifecycleEvent(t, eventsCh, created.ID, p.ID, wire.SessionEventActionDeleted)
}

func TestProjectRemovalRetiresSessions(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	srv := newTestServer(t)
	srv.sessions.SetProjectRegistry(srv.projectRegistry)
	projectID, sessions := createLifecycleFixture(t, srv, []string{"doomed"})

	deleteProject(t, srv, projectID)

	get := newAuthedRequest(http.MethodGet, "/v1/sessions/"+sessions[0].ID, nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, get)
	assertErrorResponse(t, getRec, http.StatusNotFound, "session_not_found")
}

func assertSessionLifecycleEvent(
	t *testing.T,
	eventsCh <-chan wire.EventEnvelope,
	sessionID string,
	projectID string,
	action wire.SessionEventAction,
) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case envelope := <-eventsCh:
			if envelope.Topic != wire.EventTopicSession {
				continue
			}
			var event wire.SessionEvent
			testutil.FailErr(t, "decode session event", json.Unmarshal(envelope.Data, &event))
			if event.ID != sessionID || event.ProjectID != projectID || event.Action != action {
				t.Fatalf("session lifecycle event = %#v envelope = %#v", event, envelope)
			}
			return
		case <-timer.C:
			t.Fatalf("timed out waiting for session lifecycle action %q", action)
		}
	}
}
