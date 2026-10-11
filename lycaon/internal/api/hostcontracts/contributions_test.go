package hostcontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestContributionsUnavailableWithoutCompiledCatalog(t *testing.T) {
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{UserNotices: contractfixture.TestUserNotices(t)}}), nil, hostapi.TestAPIToken)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/contributions", nil))
	contractfixture.AssertErrorResponse(t, w, http.StatusServiceUnavailable, "contributions_unavailable")
}

// Appearance is device configuration, so the frame it selects from is reachable
// without a project.

func TestListContributionsAcceptsAbsentProjectIDAsDeviceScope(t *testing.T) {
	srv := contractfixture.NewTestServer(t)

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/contributions", nil)
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
	srv := contractfixture.NewContributionHTTPTestServer(t)
	first := httptest.NewRecorder()
	srv.ServeHTTP(first, contractfixture.NewAuthedRequest(http.MethodGet, "/v1/contributions", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first contribution frame status=%d body=%s", first.Code, first.Body.String())
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("first contribution frame omitted ETag")
	}

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/contributions", nil)
	req.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	srv.ServeHTTP(second, req)
	if second.Code != http.StatusNotModified || second.Body.Len() != 0 {
		t.Fatalf("conditional frame status=%d body=%s", second.Code, second.Body.String())
	}
}

func TestListContributionsIgnoresProjectIDQuery(t *testing.T) {
	srv := contractfixture.NewContributionHTTPTestServer(t)

	req := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/contributions?project_id=nope", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK && w.Code != http.StatusNotModified {
		t.Fatalf("project_id query must not scope the frame: status=%d body=%s", w.Code, w.Body.String())
	}
}
