package sessionadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/chats"
	"github.com/lycaon/lycaon/internal/session/checkpointcontrol"
	"github.com/lycaon/lycaon/internal/session/naming"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil/apitestdeps"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSessionMutationsRejectInvalidRequestsBeforeUsingSessionServices(t *testing.T) {
	responses := &httpio.Responder{}
	lifecycle := &Lifecycle{responses: responses}
	rewind := &Rewind{responses: responses}
	id := "123e4567-e89b-12d3-a456-426614174000"
	cases := []struct {
		name, body string
		handler    http.HandlerFunc
		code       wire.ApiErrorCode
	}{
		{"patch no field", "{}", lifecycle.HandleUpdateSession, wire.ApiErrorCodeInvalidRequest},
		{"patch two fields", `{"title":"Title","pinned":true}`, lifecycle.HandleUpdateSession, wire.ApiErrorCodeInvalidRequest},
		{"negative pin", `{"pin_position":0}`, lifecycle.HandleUpdateSession, wire.ApiErrorCodeInvalidRequest},
		{"rewind operation", "{}", rewind.HandleRewindSession, wire.ApiErrorCodeInvalidRequest},
		{"rewind message", `{"operation_id":"` + id + `"}`, rewind.HandleRewindSession, wire.ApiErrorCodeInvalidRequest},
		{"rewind digest", `{"operation_id":"` + id + `","message_id":"` + id + `"}`, rewind.HandleRewindSession, wire.ApiErrorCodeInvalidRequest},
		{"rewind mode", `{"operation_id":"` + id + `","message_id":"` + id + `","plan_digest":"digest","mode":"unsupported"}`, rewind.HandleRewindSession, wire.ApiErrorCodeRewindModeUnsupported},
		{"rewind preview", "{}", rewind.HandlePreviewSessionRewind, wire.ApiErrorCodeInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/sessions/session", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			tc.handler(rec, req)
			assertSessionRefusal(t, rec, tc.code)
		})
	}
}

func TestSessionRefusalsPreserveWrappedDomainFailureCodes(t *testing.T) {
	responses := &httpio.Responder{}
	lifecycle := &Lifecycle{responses: responses}
	rewind := &Rewind{responses: responses}
	cases := []struct {
		failure error
		code    wire.ApiErrorCode
		rewind  bool
	}{
		{naming.ErrInvalidDisplayTitle, wire.ApiErrorCodeInvalidRequest, false},
		{chats.ErrWorkerChildLifecycle, wire.ApiErrorCodeInvalidRequest, false},
		{store.ErrSessionArchived, wire.ApiErrorCodeSessionArchived, false},
		{store.ErrSessionNotPinned, wire.ApiErrorCodeSessionNotPinned, false},
		{chats.ErrSessionBusy, wire.ApiErrorCodeSessionNotIdle, false},
		{chats.ErrSessionWorktreeBound, wire.ApiErrorCodeWorktreeAlreadyBound, false},
		{store.ErrSessionNotFound, wire.ApiErrorCodeSessionNotFound, false},
		{&sourceledger.RewindBlockedError{}, wire.ApiErrorCodeRewindBlocked, true},
		{checkpointcontrol.ErrRewindPlanChanged, wire.ApiErrorCodeRewindPlanChanged, true},
		{checkpointcontrol.ErrSessionNotIdle, wire.ApiErrorCodeSessionNotIdle, true},
		{checkpointcontrol.ErrRewindAnchorIneligible, wire.ApiErrorCodeRewindAnchorIneligible, true},
		{checkpointcontrol.ErrRewindAnchorNotFound, wire.ApiErrorCodeRewindAnchorNotFound, true},
		{checkpointcontrol.ErrRewindOperationConflict, wire.ApiErrorCodeIdempotencyConflict, true},
		{store.ErrSessionNotFound, wire.ApiErrorCodeSessionNotFound, true},
	}
	for _, tc := range cases {
		t.Run(string(tc.code)+fmt.Sprint(tc.rewind), func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/sessions/session", nil)
			wrapped := fmt.Errorf("operation failed: %w", tc.failure)
			if tc.rewind {
				rewind.writeRewindError(rec, req, wrapped)
			} else {
				lifecycle.writeSessionLifecycleError(rec, req, wrapped)
			}
			assertSessionRefusal(t, rec, tc.code)
		})
	}
}

func assertSessionRefusal(t *testing.T, rec *httptest.ResponseRecorder, want wire.ApiErrorCode) {
	t.Helper()
	var response wire.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Code != want || rec.Code != want.HTTPStatus() {
		t.Fatalf("status=%d code=%s want=%s", rec.Code, response.Code, want)
	}
}

func TestUnavailableWorkerNavigationClearsStaleCandidatesWithoutMutatingInput(t *testing.T) {
	refs := []wire.NavigationReference{{Status: wire.NavigationAmbiguous, Deleted: true, Candidates: []wire.NavigationTarget{{WorkerID: "old"}}}}
	nav := &Navigation{}
	out := nav.ResolveNavigationJob(t.Context(), &project.Project{ID: "project"}, "unavailable-worker", refs)
	if len(out) != 1 || out[0].Status != wire.NavigationUnavailable || out[0].Deleted || out[0].WorkerID != "unavailable-worker" || out[0].Candidates != nil {
		t.Fatalf("unavailable navigation=%+v", out)
	}
	if refs[0].Status != wire.NavigationAmbiguous || !refs[0].Deleted || len(refs[0].Candidates) != 1 {
		t.Fatal("refusal mutated stored references")
	}
}

func TestAdmittedSessionMutationsRetainMissingChatRefusal(t *testing.T) {
	deps := apitestdeps.Deps{}
	apitestdeps.Fill(t, &deps)
	lifecycle := &Lifecycle{Sessions: deps.Sessions, responses: &httpio.Responder{Logger: slog.New(slog.DiscardHandler)}}
	for _, body := range []string{`{"title":"Valid title"}`, `{"archived":true}`, `{"pinned":true}`, `{"pin_position":1}`} {
		req := httptest.NewRequest(http.MethodPatch, "/v1/sessions/missing", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		route := chi.NewRouteContext()
		route.URLParams.Add("id", "missing")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		rec := httptest.NewRecorder()
		lifecycle.HandleUpdateSession(rec, req)
		assertSessionRefusal(t, rec, wire.ApiErrorCodeSessionNotFound)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/v1/sessions/missing", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("id", "missing")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	lifecycle.HandleDeleteSession(rec, req)
	assertSessionRefusal(t, rec, wire.ApiErrorCodeSessionNotFound)
}

func TestSessionReadsAndAbortRefuseMissingChatBeforeProjection(t *testing.T) {
	deps := apitestdeps.Deps{}
	apitestdeps.Fill(t, &deps)
	responses := &httpio.Responder{Logger: slog.New(slog.DiscardHandler)}
	bootstrap := &Bootstrap{Store: deps.Store, responses: responses}
	transcript := &Transcript{Store: deps.Store, responses: responses}
	lifecycle := &Lifecycle{Sessions: deps.Sessions, responses: responses}
	navigation := &Navigation{Store: deps.Store, responses: responses}
	for _, tc := range []struct {
		handle http.HandlerFunc
		body   string
	}{{bootstrap.HandleSessionBootstrap, ""}, {transcript.HandleMarkSessionSeen, ""}, {lifecycle.HandleAbortSession, ""}, {navigation.HandleResolveMessageNavigation, `{"message_id":"missing","content_sha256":"` + strings.Repeat("a", 64) + `"}`}} {
		req := httptest.NewRequest(http.MethodPost, "/v1/sessions/missing", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		route := chi.NewRouteContext()
		route.URLParams.Add("id", "missing")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		rec := httptest.NewRecorder()
		tc.handle(rec, req)
		assertSessionRefusal(t, rec, wire.ApiErrorCodeSessionNotFound)
	}
	workers, checkpoints, err := bootstrap.sessionBootstrapWorkersAndCheckpoints(t.Context(), "project", "session")
	if err != nil || workers == nil || checkpoints == nil || len(workers) != 0 || len(checkpoints) != 0 {
		t.Fatalf("optional bootstrap services invented work: workers=%v checkpoints=%v err=%v", workers, checkpoints, err)
	}
}
