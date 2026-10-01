//go:build integration

package app

import (
	"github.com/lycaon/lycaon/internal/configlayout"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestServeAppHTTptestProjects(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "integration-test-token")

	cfg := testBuildConfig(t, configlayout.FindModuleRoot())
	serveApp, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "Build failed", err)
	defer func() { _ = serveApp.Close() }()

	ts := httptest.NewServer(serveApp.Server)
	defer ts.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/v1/projects", nil)
	testutil.FailErr(t, "http.NewRequestWithContext failed", err)
	req.Header.Set("Authorization", "Bearer integration-test-token")

	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, "http.DefaultClient.Do failed", err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/projects status = %d, want 200", resp.StatusCode)
	}
}
