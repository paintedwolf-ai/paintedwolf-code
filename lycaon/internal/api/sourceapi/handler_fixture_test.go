package sourceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/gitadmin"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/apitestdeps"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func callSourceViewHandler(t *testing.T, handler http.HandlerFunc, projectID, viewID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	testutil.FailErr(t, "encode source view request", err)
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	route := chi.NewRouteContext()
	route.URLParams.Add("id", projectID)
	route.URLParams.Add("view_id", viewID)
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, route)
	ctx = people.WithCaller(ctx, testutil.HostOwner())
	response := httptest.NewRecorder()
	handler(response, request.WithContext(ctx))
	return response
}

func readSourceViewResponse(t *testing.T, response *httptest.ResponseRecorder, status int) wire.SourceView {
	t.Helper()
	if response.Code != status {
		t.Fatalf("source view status=%d body=%s", response.Code, response.Body.String())
	}
	var view wire.SourceView
	testutil.FailErr(t, "decode source view", json.Unmarshal(response.Body.Bytes(), &view))
	return view
}

func newSourceHandlerFixture(t *testing.T, opts ...func(*Deps)) *Handler {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	group := &taskgroup.Group{}
	deps := Deps{
		SessionStore:    store.NewMemory(),
		Git:             &gitadmin.Handler{},
		ProjectRegistry: project.NewMemoryRegistry(),
	}
	for _, opt := range opts {
		opt(&deps)
	}
	required := apitestdeps.Deps{
		Store: deps.SessionStore, Projects: deps.ProjectRegistry, EditorDocuments: deps.EditorDocuments,
		FileBriefings: deps.FileBriefings, ManagedSecrets: deps.ManagedSecrets, MutationGate: deps.MutationGate,
		SourceLedger: deps.SourceLedger, SourceMutations: deps.SourceMutations, FileOperations: deps.FileOperations,
		Workers: deps.Workers,
	}
	apitestdeps.Fill(t, &required)
	deps.EditorDocuments, deps.FileBriefings, deps.ManagedSecrets = required.EditorDocuments, required.FileBriefings, required.ManagedSecrets
	deps.MutationGate, deps.SourceLedger = required.MutationGate, required.SourceLedger
	deps.SourceMutations, deps.FileOperations = required.SourceMutations, required.FileOperations
	deps.Workers = required.Workers
	handler := New(&httpio.Responder{Logger: slog.Default()}, group, Operations{}, deps)
	t.Cleanup(func() {
		group.Stop()
		handler.Watch.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		group.Wait(ctx)
		testutil.FailErr(t, "drain source watches", handler.Watch.Wait(ctx))
		testutil.FailErr(t, "drain source background", ctx.Err())
	})
	return &handler
}
