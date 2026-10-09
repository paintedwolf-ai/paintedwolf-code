package smoke_test

import (
	"github.com/lycaon/lycaon/internal/app/configuration"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/app"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAppBuildBoot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping smoke test in short mode")
	}

	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "smoke-build-token")

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	moduleRoot := filepath.Join(filepath.Dir(file), "..", "..")

	cfg := configuration.Config{}
	cfg.DBPath = filepath.Join(t.TempDir(), "smoke-build.db")
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.ConfigRoot = moduleRoot

	serveApp, err := app.Build(t.Context(), cfg)
	testutil.FailErr(t, "build app wiring harness", err)
	defer func() { _ = serveApp.Close() }()

	ts := httptest.NewServer(serveApp.Server)
	defer ts.Close()

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/health", nil)
	resp, err := http.DefaultClient.Do(req)
	testutil.FailErr(t, "http.DefaultClient.Do failed", err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /health status = %d, want 200", resp.StatusCode)
	}
}
