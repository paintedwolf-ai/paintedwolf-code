package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/testutil"
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

func TestSourceViewCreateReplayAndComparisonCommands(t *testing.T) {
	server := newTestServer(t)
	t.Cleanup(server.StopBackground)
	p := createProjectForTest(t, server, t.TempDir())
	before, after := "before\n", "after\n"
	request := wire.SourceComparisonViewCreate{Kind: "comparison", ClientID: "window:main", OperationID: uuid.NewString(),
		Source: wire.SourceComparisonSelector{Text: &wire.TextComparisonSource{Kind: "text", Path: "a.txt", Before: &before, After: &after}},
		Intent: wire.SourceComparisonIntent{Mode: "full"}}
	created := readSourceViewResponse(t, callSourceViewHandler(t, server.Sources.HandleCreateSourceView, p.ID, "", request), http.StatusCreated)
	id := created.Comparison.ID
	replay := readSourceViewResponse(t, callSourceViewHandler(t, server.Sources.HandleCreateSourceView, p.ID, "", request), http.StatusCreated)
	if replay.Comparison.ID != id {
		t.Fatal("create retry made another view")
	}
	changed := request
	changed.Intent.Mode = "changes"
	conflict := callSourceViewHandler(t, server.Sources.HandleCreateSourceView, p.ID, "", changed)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("request body conflict=%d %s", conflict.Code, conflict.Body.String())
	}
	var ready wire.SourceView
	testutil.WaitFor(t, 10*time.Second, func() bool {
		ready = readSourceViewResponse(t, callSourceViewHandler(t, server.Sources.HandleGetSourceView, p.ID, id, nil), http.StatusOK)
		return ready.Comparison.State != "preparing"
	})
	if ready.Comparison.State != "ready" {
		t.Fatalf("preparation failed: %+v", ready.Comparison.Failure)
	}
	update := wire.SourceComparisonViewUpdate{Kind: "comparison", OperationID: uuid.NewString(), ExpectedIntentRevision: ready.Comparison.IntentRevision, Intent: wire.SourceComparisonIntent{Mode: "before"}}
	updated := readSourceViewResponse(t, callSourceViewHandler(t, server.Sources.HandleApplySourceViewIntent, p.ID, id, update), http.StatusOK)
	repeated := readSourceViewResponse(t, callSourceViewHandler(t, server.Sources.HandleApplySourceViewIntent, p.ID, id, update), http.StatusOK)
	if repeated.Comparison.IntentRevision != updated.Comparison.IntentRevision || repeated.Comparison.Intent.Mode != "before" {
		t.Fatal("repeated command changed intent twice")
	}
	update.OperationID = uuid.NewString()
	stale := callSourceViewHandler(t, server.Sources.HandleApplySourceViewIntent, p.ID, id, update)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale intent accepted: %d", stale.Code)
	}
	released := callSourceViewHandler(t, server.Sources.HandleReleaseSourceView, p.ID, id, nil)
	if released.Code != http.StatusNoContent {
		t.Fatalf("release=%d %s", released.Code, released.Body.String())
	}
	if again := callSourceViewHandler(t, server.Sources.HandleReleaseSourceView, p.ID, id, nil); again.Code != http.StatusNotFound {
		t.Fatalf("second release=%d %s", again.Code, again.Body.String())
	}
	expired := callSourceViewHandler(t, server.Sources.HandleGetSourceView, p.ID, id, nil)
	if expired.Code != http.StatusNotFound {
		t.Fatalf("released view=%d", expired.Code)
	}
}

func TestSourceViewInvalidationReleasesOnlyChangedProject(t *testing.T) {
	server := newTestServer(t)
	t.Cleanup(server.StopBackground)
	first := createProjectForTest(t, server, t.TempDir())
	second := createProjectForTest(t, server, t.TempDir())
	text := "source\n"
	request := wire.SourceComparisonViewCreate{Kind: "comparison", ClientID: "window:main", OperationID: uuid.NewString(), Source: wire.SourceComparisonSelector{Text: &wire.TextComparisonSource{Kind: "text", Path: "a.txt", After: &text}}, Intent: wire.SourceComparisonIntent{Mode: "full"}}
	firstView := readSourceViewResponse(t, callSourceViewHandler(t, server.Sources.HandleCreateSourceView, first.ID, "", request), http.StatusCreated)
	secondView := readSourceViewResponse(t, callSourceViewHandler(t, server.Sources.HandleCreateSourceView, second.ID, "", request), http.StatusCreated)
	server.Sources.InvalidateProjectSourceViews(first.ID)
	removed := callSourceViewHandler(t, server.Sources.HandleGetSourceView, first.ID, firstView.Comparison.ID, nil)
	if removed.Code != http.StatusNotFound {
		t.Fatalf("invalidated view=%d", removed.Code)
	}
	readSourceViewResponse(t, callSourceViewHandler(t, server.Sources.HandleGetSourceView, second.ID, secondView.Comparison.ID, nil), http.StatusOK)
}

func TestTextComparisonDistinguishesAbsentFromEmpty(t *testing.T) {
	empty := ""
	absent, present := sourceapi.TextComparisonSide("empty.txt", nil), sourceapi.TextComparisonSide("empty.txt", &empty)
	if absent.State != "absent" || absent.Availability != "absent" {
		t.Fatalf("absent side: %+v", absent)
	}
	if present.State == "absent" || present.Availability != "available" || present.Content != "" {
		t.Fatalf("empty side: %+v", present)
	}
}
