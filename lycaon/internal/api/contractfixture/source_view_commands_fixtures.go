package contractfixture

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func CallSourceViewHandler(t *testing.T, handler http.HandlerFunc, projectID, viewID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	testutil.FailErr(t, "encode source view request", err)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", bytes.NewReader(encoded))
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

func ReadSourceViewResponse(t *testing.T, response *httptest.ResponseRecorder, status int) wire.SourceView {
	t.Helper()
	if response.Code != status {
		t.Fatalf("source view status=%d body=%s", response.Code, response.Body.String())
	}
	var view wire.SourceView
	testutil.FailErr(t, "decode source view", json.Unmarshal(response.Body.Bytes(), &view))
	return view
}
