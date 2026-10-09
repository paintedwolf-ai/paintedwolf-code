package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

func newContributionHTTPTestServer(t *testing.T) *Server {
	t.Helper()
	srv := newTestServer(t)
	root := configlayout.FindModuleRoot()
	boot := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs: func() []extpacks.PackContent {
			content, err := extpacks.DiscoverStockContent()
			testutil.FailErr(t, "discover stock content", err)
			return content
		}(),
		Desired: extpacks.EmptyDesired(),
	})
	srv.sessions.SetEffectiveCatalogDeps(root, boot, nil)
	srv.sessions.Catalog.SetCatalogViewCache(catalogview.NewCache(root, slog.Default()))
	return srv
}

func TestContributionsUnavailableWithoutCompiledCatalog(t *testing.T) {
	srv := NewServer(requiredTestDeps(t, Dependencies{UserNotices: testUserNotices(t)}), nil, TestAPIToken)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, newAuthedRequest(http.MethodGet, "/v1/contributions", nil))
	assertErrorResponse(t, w, http.StatusServiceUnavailable, "contributions_unavailable")
}

// Appearance is device configuration, so the frame it selects from is reachable
// without a project.
func TestListContributionsAcceptsAbsentProjectIDAsDeviceScope(t *testing.T) {
	srv := newTestServer(t)

	req := newAuthedRequest(http.MethodGet, "/v1/contributions", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var body struct {
		Code string `json:"code"`
	}
	testutil.FailErr(t, "decode error body", json.Unmarshal(w.Body.Bytes(), &body))
	if w.Code == http.StatusBadRequest || body.Code == "invalid_request" {
		t.Fatalf("absent project_id must be device scope, not a bad request: status=%d body=%s",
			w.Code, w.Body.String())
	}
}

func TestListContributionsReturnsNotModifiedForCurrentFrame(t *testing.T) {
	srv := newContributionHTTPTestServer(t)
	first := httptest.NewRecorder()
	srv.ServeHTTP(first, newAuthedRequest(http.MethodGet, "/v1/contributions", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first contribution frame status=%d body=%s", first.Code, first.Body.String())
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("first contribution frame omitted ETag")
	}

	req := newAuthedRequest(http.MethodGet, "/v1/contributions", nil)
	req.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	srv.ServeHTTP(second, req)
	if second.Code != http.StatusNotModified || second.Body.Len() != 0 {
		t.Fatalf("conditional frame status=%d body=%s", second.Code, second.Body.String())
	}
}

func TestListContributionsIgnoresProjectIDQuery(t *testing.T) {
	srv := newContributionHTTPTestServer(t)

	req := newAuthedRequest(http.MethodGet, "/v1/contributions?project_id=nope", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK && w.Code != http.StatusNotModified {
		t.Fatalf("project_id query must not scope the frame: status=%d body=%s", w.Code, w.Body.String())
	}
}
