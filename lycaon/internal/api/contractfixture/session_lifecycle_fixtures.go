package contractfixture

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func AssertSessionLifecycleEvent(
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

func CreateLifecycleFixture(t *testing.T, srv *hostapi.Server, titles []string) (string, []wire.Session) {
	t.Helper()
	createProj := NewAuthedRequest(http.MethodPost, "/v1/projects", strings.NewReader(`{"roots":[]}`))
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
		createSess := NewAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(
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
		DrainBackground(t, srv)
		patch := NewAuthedRequest(http.MethodPatch, "/v1/sessions/"+sess.ID, strings.NewReader(
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

func ListSessionsPage(t *testing.T, srv *hostapi.Server, projectID, query string) wire.SessionListPage {
	t.Helper()
	req := NewAuthedRequest(http.MethodGet, "/v1/projects/"+projectID+"/sessions"+query, nil)
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

func PatchSession(t *testing.T, srv *hostapi.Server, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, NewAuthedRequest(http.MethodPatch, "/v1/sessions/"+id, strings.NewReader(body)))
	return rec
}

func PinnedTitles(t *testing.T, srv *hostapi.Server, projectID string) []string {
	t.Helper()
	page := ListSessionsPage(t, srv, projectID, "?pinned=true&sort=pin")
	titles := make([]string, 0, len(page.Sessions))
	for _, row := range page.Sessions {
		titles = append(titles, row.Title)
	}
	return titles
}
