//go:build integration

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHandleSearchExportJSONLAcrossProjects(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB, srv := newSearchExportServer(t)
	seedSearchRow(t, sqlDB, search.IndexRow{
		ID: "a1", ProjectID: "proj-a", Source: search.SourceTool, HitKind: search.HitKindWeb,
		Snippet: "export-shared-token", TS: formatSearchTS(time.Now().UTC()),
	})
	seedSearchRow(t, sqlDB, search.IndexRow{
		ID: "b1", ProjectID: "proj-b", Source: search.SourceTool, HitKind: search.HitKindWeb,
		Snippet: "export-shared-token", TS: formatSearchTS(time.Now().UTC()),
	})

	body := `{"query":"export-shared-token","format":"jsonl"}`
	rec := postSearchExport(t, srv, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/x-ndjson") {
		t.Fatalf("content-type = %q", got)
	}
	text := rec.Body.String()
	if !strings.Contains(text, `"project_id":"proj-a"`) || !strings.Contains(text, `"project_id":"proj-b"`) {
		t.Fatalf("body = %s", text)
	}
}

func TestHandleSearchExportCSVIncludesProjectName(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB, srv := newSearchExportServer(t)
	seedSearchRow(t, sqlDB, search.IndexRow{
		ID: "a1", ProjectID: "proj-a", Source: search.SourceTool, HitKind: search.HitKindWeb,
		Snippet: "export-csv-token", TS: formatSearchTS(time.Now().UTC()),
	})

	body := `{"query":"export-csv-token","format":"csv","origin_project_id":"proj-a"}`
	rec := postSearchExport(t, srv, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "proj-a") {
		t.Fatalf("csv body = %s", rec.Body.String())
	}
}

func TestHandleSearchExportSARIFRequiresScanScope(t *testing.T) {
	_, srv := newSearchExportServer(t)
	body := `{"query":"export-shared-token","format":"sarif"}`
	rec := postSearchExport(t, srv, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "search_export_sarif_scope") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestHandleSearchExportSARIFScanScoped(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB, srv := newSearchExportServer(t)
	seedSearchRow(t, sqlDB, search.IndexRow{
		ID: "scan-1", ProjectID: "proj-a", Source: search.SourceFinding, HitKind: search.HitKindFinding,
		Kind: "scan", Shape: "artifact", Handle: "opengrep:test", Path: "src/a.go", Line: 4,
		Snippet: "scan export finding", TS: formatSearchTS(time.Now().UTC()),
	})

	body := `{"query":"kind:scan","format":"sarif","origin_project_id":"proj-a"}`
	rec := postSearchExport(t, srv, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "opengrep:test") {
		t.Fatalf("sarif body = %s", rec.Body.String())
	}
}

func TestHandleSearchExportInvalidFormat(t *testing.T) {
	_, srv := newSearchExportServer(t)
	body := `{"query":"token","format":"xml"}`
	rec := postSearchExport(t, srv, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "search_export_invalid_format") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestHandleSearchExportTruncationHeaderFalseForSmallResult(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB, srv := newSearchExportServer(t)
	seedSearchRow(t, sqlDB, search.IndexRow{
		ID: "a1", ProjectID: "proj-a", Source: search.SourceTool, HitKind: search.HitKindWeb,
		Snippet: "small-export-token", TS: formatSearchTS(time.Now().UTC()),
	})
	body := `{"query":"small-export-token","format":"jsonl","origin_project_id":"proj-a"}`
	rec := postSearchExport(t, srv, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Export-Truncated") != "false" {
		t.Fatalf("truncated header = %q", rec.Header().Get("X-Export-Truncated"))
	}
}

func newSearchExportServer(t *testing.T) (db.Handle, *Server) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	roots := map[string]sourcecatalog.Root{}
	for _, projectID := range []string{"proj-a", "proj-b"} {
		dir := t.TempDir()
		roots[projectID] = sourcecatalog.Root{ID: testdbseed.InsertProjectRoot(t, sqlDB, projectID, dir), Path: dir}
	}
	store := store.NewSQL(sqlDB)
	reg := project.NewSQLRegistry(sqlDB)
	srv := NewServer(requiredTestDeps(t, Dependencies{Store: store, Projects: reg}), nil, TestAPIToken)
	// Unscoped exports also search every attached root's source; an unsettled
	// root makes the code leg report partial coverage, which export marks truncated.
	for projectID, root := range roots {
		testutil.FailErr(t, "settle source inventory for "+projectID,
			catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), projectID, root))
	}
	return sqlDB, srv
}

func postSearchExport(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := newAuthedRequest(http.MethodPost, "/v1/search/export", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}
