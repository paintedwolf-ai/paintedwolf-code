package api

import (
	"archive/zip"
	"bytes"
	"github.com/lycaon/lycaon/internal/configlayout"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
)

// This exercises the authenticated HTTP route rather than calling Build
// directly: the running-engine export must pass its configured catalog root
// and data directory through to the archive builder.
func TestDiagnosticsExportRouteReturnsRedactedArchive(t *testing.T) {
	dataDir := t.TempDir()
	const configSecret = "route-config-provider-secret"
	const logSecret = "route-log-provider-secret"
	for name, body := range map[string]string{
		"settings.yaml":        "OPENAI_API_KEY: " + configSecret + "\nposture: balanced\n",
		"engine.log":           "starting\nGEMINI_API_KEY=" + logSecret + "\nfailed\n",
		"credential-vault.age": "excluded-secret\n",
	} {
		err := os.WriteFile(filepath.Join(dataDir, name), []byte(body), 0o600)
		testutil.FailErr(t, "write diagnostics route fixture "+name, err)
	}

	srv := NewServer(requiredTestDeps(t, Dependencies{
		Store: store.NewMemory(), Projects: project.NewMemoryRegistry(),
		ModuleRoot: configlayout.FindModuleRoot(), DataDir: dataDir,
	}), nil, TestAPIToken)
	req := newAuthedRequest(http.MethodGet, "/v1/diagnostics/export", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("diagnostics export status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/zip" {
		t.Fatalf("content type = %q want application/zip", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "painted-wolf-code-diagnostics-") {
		t.Fatalf("content disposition = %q missing archive filename", got)
	}
	for _, secret := range []string{configSecret, logSecret, "excluded-secret"} {
		if bytes.Contains(rec.Body.Bytes(), []byte(secret)) {
			t.Fatalf("HTTP diagnostics archive leaked %q", secret)
		}
	}

	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	testutil.FailErr(t, "open HTTP diagnostics archive", err)
	entries := make(map[string]struct{}, len(zr.File))
	for _, file := range zr.File {
		entries[file.Name] = struct{}{}
	}
	for _, want := range []string{"manifest.json", "health.json", "preflight.json", "config/settings.yaml", "logs/engine.log"} {
		if _, ok := entries[want]; !ok {
			t.Fatalf("HTTP diagnostics archive missing %q: %v", want, entries)
		}
	}
	if _, ok := entries["config/credential-vault.age"]; ok {
		t.Fatal("HTTP diagnostics archive included the credential vault")
	}
}
