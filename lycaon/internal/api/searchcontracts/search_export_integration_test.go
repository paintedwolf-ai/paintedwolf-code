//go:build integration

package searchcontracts

import (
	"net/http"
	"strings"
	"testing"
	"time"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/search"
)

func TestHandleSearchExportJSONLAcrossProjects(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB, srv := contractfixture.NewSearchExportServer(t)
	contractfixture.SeedSearchRow(t, sqlDB, search.IndexRow{
		ID: "a1", ProjectID: "proj-a", Source: search.SourceTool, HitKind: search.HitKindWeb,
		Snippet: "export-shared-token", TS: contractfixture.FormatSearchTS(time.Now().UTC()),
	})
	contractfixture.SeedSearchRow(t, sqlDB, search.IndexRow{
		ID: "b1", ProjectID: "proj-b", Source: search.SourceTool, HitKind: search.HitKindWeb,
		Snippet: "export-shared-token", TS: contractfixture.FormatSearchTS(time.Now().UTC()),
	})

	body := `{"query":"export-shared-token","format":"jsonl"}`
	rec := contractfixture.PostSearchExport(t, srv, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/x-ndjson") {
		t.Fatalf("content-type = %q", got)
	}
	Text := rec.Body.String()
	if !strings.Contains(Text, `"project_id":"proj-a"`) || !strings.Contains(Text, `"project_id":"proj-b"`) {
		t.Fatalf("body = %s", Text)
	}
}

func TestHandleSearchExportCSVIncludesProjectName(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB, srv := contractfixture.NewSearchExportServer(t)
	contractfixture.SeedSearchRow(t, sqlDB, search.IndexRow{
		ID: "a1", ProjectID: "proj-a", Source: search.SourceTool, HitKind: search.HitKindWeb,
		Snippet: "export-csv-token", TS: contractfixture.FormatSearchTS(time.Now().UTC()),
	})

	body := `{"query":"export-csv-token","format":"csv","origin_project_id":"proj-a"}`
	rec := contractfixture.PostSearchExport(t, srv, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "proj-a") {
		t.Fatalf("csv body = %s", rec.Body.String())
	}
}

func TestHandleSearchExportSARIFRequiresScanScope(t *testing.T) {
	_, srv := contractfixture.NewSearchExportServer(t)
	body := `{"query":"export-shared-token","format":"sarif"}`
	rec := contractfixture.PostSearchExport(t, srv, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "search_export_sarif_scope") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestHandleSearchExportSARIFScanScoped(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB, srv := contractfixture.NewSearchExportServer(t)
	contractfixture.SeedSearchRow(t, sqlDB, search.IndexRow{
		ID: "scan-1", ProjectID: "proj-a", Source: search.SourceFinding, HitKind: search.HitKindFinding,
		Kind: "scan", Shape: "artifact", Handle: "opengrep:test", Path: "src/a.go", Line: 4,
		Snippet: "scan export finding", TS: contractfixture.FormatSearchTS(time.Now().UTC()),
	})

	body := `{"query":"kind:scan","format":"sarif","origin_project_id":"proj-a"}`
	rec := contractfixture.PostSearchExport(t, srv, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "opengrep:test") {
		t.Fatalf("sarif body = %s", rec.Body.String())
	}
}

func TestHandleSearchExportInvalidFormat(t *testing.T) {
	_, srv := contractfixture.NewSearchExportServer(t)
	body := `{"query":"token","format":"xml"}`
	rec := contractfixture.PostSearchExport(t, srv, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "search_export_invalid_format") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestHandleSearchExportTruncationHeaderFalseForSmallResult(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	sqlDB, srv := contractfixture.NewSearchExportServer(t)
	contractfixture.SeedSearchRow(t, sqlDB, search.IndexRow{
		ID: "a1", ProjectID: "proj-a", Source: search.SourceTool, HitKind: search.HitKindWeb,
		Snippet: "small-export-token", TS: contractfixture.FormatSearchTS(time.Now().UTC()),
	})
	body := `{"query":"small-export-token","format":"jsonl","origin_project_id":"proj-a"}`
	rec := contractfixture.PostSearchExport(t, srv, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Export-Truncated") != "false" {
		t.Fatalf("truncated header = %q", rec.Header().Get("X-Export-Truncated"))
	}
}
