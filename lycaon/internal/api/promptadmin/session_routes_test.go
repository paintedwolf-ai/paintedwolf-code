package promptadmin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/apitestdeps"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type sessionRoutes struct {
	router    http.Handler
	sessionID string
}

// newSessionRoutes mounts the content and queue routes over one real session.
func newSessionRoutes(t *testing.T) sessionRoutes {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	var deps apitestdeps.Deps
	apitestdeps.Fill(t, &deps)
	p, err := project.CreateWithRoot(t.Context(), deps.Projects, t.TempDir())
	testutil.FailErr(t, "create project", err)
	sess, err := deps.Store.Create(t.Context(), wire.CreateSessionRequest{ProjectID: p.ID, Posture: wire.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create session", err)

	responses := &httpio.Responder{Logger: slog.Default()}
	content := &Content{Sessions: deps.Sessions, Store: deps.Store, responses: responses}
	background := &taskgroup.Group{}
	t.Cleanup(func() { background.Stop(); background.Wait(context.Background()) })
	queue := &Queue{Sessions: deps.Sessions, Store: deps.Store, background: background, responses: responses}
	r := chi.NewRouter()
	r.Post("/v1/sessions/{id}/compact", content.HandleSessionCompact)
	r.Get("/v1/sessions/{id}/context", content.HandleSessionContext)
	r.Get("/v1/sessions/{id}/coordinator-context", content.HandleCoordinatorContext)
	r.Get("/v1/sessions/{id}/drafts/{slot_id}/versions", content.HandleListDraftVersions)
	r.Get("/v1/sessions/{id}/queue", queue.HandleGetQueue)
	r.Patch("/v1/sessions/{id}/queue", queue.HandleUpdateSessionQueue)
	return sessionRoutes{router: r, sessionID: sess.ID}
}

func (s sessionRoutes) serve(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

func responseCode(t *testing.T, w *httptest.ResponseRecorder) wire.ApiErrorCode {
	t.Helper()
	var body wire.ErrorResponse
	testutil.FailErr(t, "decode error body", json.Unmarshal(w.Body.Bytes(), &body))
	return body.Code
}

func TestSessionContentRoutesRefuseAnUnknownSession(t *testing.T) {
	s := newSessionRoutes(t)
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/v1/sessions/missing/compact"},
		{http.MethodGet, "/v1/sessions/missing/context"},
		{http.MethodGet, "/v1/sessions/missing/coordinator-context"},
		{http.MethodGet, "/v1/sessions/missing/drafts/slot/versions"},
		{http.MethodGet, "/v1/sessions/missing/queue"},
		{http.MethodPatch, "/v1/sessions/missing/queue"},
	} {
		body := ""
		if route.method == http.MethodPatch {
			body = `{"op":"send","expected_revision":0}`
		}
		w := s.serve(t, route.method, route.path, body)
		if got := responseCode(t, w); got != wire.ApiErrorCodeSessionNotFound {
			t.Fatalf("%s %s code = %q, want session_not_found (body %s)", route.method, route.path, got, w.Body.String())
		}
	}
}

func TestDraftVersionsNeedTheSupersededMessage(t *testing.T) {
	s := newSessionRoutes(t)
	w := s.serve(t, http.MethodGet, "/v1/sessions/"+s.sessionID+"/drafts/no-such-message/versions", "")
	if got := responseCode(t, w); got != wire.ApiErrorCodeMessageNotFound {
		t.Fatalf("code = %q, want message_not_found (body %s)", got, w.Body.String())
	}
}

func TestSessionQueueReadsAndRefusesMalformedEdits(t *testing.T) {
	s := newSessionRoutes(t)
	queuePath := "/v1/sessions/" + s.sessionID + "/queue"
	read := s.serve(t, http.MethodGet, queuePath, "")
	if read.Code != http.StatusOK {
		t.Fatalf("queue read status = %d body = %s", read.Code, read.Body.String())
	}
	var draft wire.QueueDraft
	testutil.FailErr(t, "decode queue", json.Unmarshal(read.Body.Bytes(), &draft))
	if len(draft.QueueItems) != 0 {
		t.Fatalf("a new session queued %+v", draft.QueueItems)
	}
	if got := responseCode(t, s.serve(t, http.MethodPatch, queuePath, `{"op":"send","expected_revision":0}`)); got != wire.ApiErrorCodeQueueEmpty {
		t.Fatalf("send without a queue = %q, want queue_empty", got)
	}

	held := s.serve(t, http.MethodPatch, queuePath, `{"op":"hold","hold":true,"expected_revision":`+itoa(draft.Revision)+`}`)
	if held.Code != http.StatusOK {
		t.Fatalf("hold status = %d body = %s", held.Code, held.Body.String())
	}
	testutil.FailErr(t, "decode held queue", json.Unmarshal(held.Body.Bytes(), &draft))
	if !draft.Hold {
		t.Fatalf("held queue = %+v", draft)
	}

	cases := []struct {
		name, body string
		want       wire.ApiErrorCode
	}{
		{"malformed body", `{"op":`, wire.ApiErrorCodeInvalidJson},
		{"unknown op", `{"op":"shuffle"}`, wire.ApiErrorCodeInvalidRequest},
		{"remove without items", `{"op":"remove"}`, wire.ApiErrorCodeInvalidRequest},
		{"update without text", `{"op":"update","item_ids":["a"]}`, wire.ApiErrorCodeInvalidRequest},
		{"hold without a value", `{"op":"hold"}`, wire.ApiErrorCodeInvalidRequest},
		{"cancel with nothing sending", `{"op":"cancel_send","expected_revision":` + itoa(draft.Revision) + `}`, wire.ApiErrorCodeQueueNoSendToCancel},
		{"send an empty queue", `{"op":"send","expected_revision":` + itoa(draft.Revision) + `}`, wire.ApiErrorCodeQueueEmpty},
		{"stale revision", `{"op":"send","expected_revision":` + itoa(draft.Revision+7) + `}`, wire.ApiErrorCodeQueueRevisionConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := s.serve(t, http.MethodPatch, queuePath, tc.body)
			if got := responseCode(t, w); got != tc.want {
				t.Fatalf("code = %q, want %q (status %d body %s)", got, tc.want, w.Code, w.Body.String())
			}
		})
	}
}

func itoa[T ~int | ~int64 | ~uint64](v T) string {
	b, _ := json.Marshal(v)
	return string(b)
}
