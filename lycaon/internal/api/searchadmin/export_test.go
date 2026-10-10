package searchadmin

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestExportSearchResultsWritesRequestedFormat(t *testing.T) {
	f := newSearchFixture(t)
	p := f.addProject(t, "Exporter", "exporter-root", map[string]string{"a.txt": "unrelated\n"})
	f.seedRow(t, search.IndexRow{ID: "e1", ProjectID: p.ID, Source: search.SourceTool, HitKind: search.HitKindWeb,
		Snippet: "export-token one"})
	f.seedRow(t, search.IndexRow{ID: "e2", ProjectID: p.ID, Source: search.SourceTool, HitKind: search.HitKindWeb,
		Snippet: "export-token two"})

	for _, format := range []string{search.ExportFormatJSONL, search.ExportFormatCSV} {
		t.Run(format, func(t *testing.T) {
			body := jsonBody(t, wire.SearchExportRequest{Query: "export-token", OriginProjectID: p.ID,
				Format: wire.SearchExportFormat(strings.ToUpper(format)), CaseSensitive: true})
			rec := f.post(t, "/v1/search/export", body)
			if rec.Code != 200 {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != search.ExportContentType(format) {
				t.Fatalf("content type = %q", got)
			}
			if rec.Header().Get("X-Export-Truncated") != "false" {
				t.Fatalf("truncated header = %q", rec.Header().Get("X-Export-Truncated"))
			}
			if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment; filename=") {
				t.Fatalf("disposition = %q", rec.Header().Get("Content-Disposition"))
			}
			if !strings.Contains(rec.Body.String(), "Exporter") {
				t.Fatalf("export lost the project name: %s", rec.Body.String())
			}
		})
	}
	t.Run("jsonl rows", func(t *testing.T) {
		rec := f.post(t, "/v1/search/export", jsonBody(t, wire.SearchExportRequest{Query: "export-token", OriginProjectID: p.ID, Format: "jsonl"}))
		rows := 0
		scanner := bufio.NewScanner(rec.Body)
		for scanner.Scan() {
			var row map[string]any
			testutil.FailErr(t, "decode jsonl row", json.Unmarshal(scanner.Bytes(), &row))
			rows++
		}
		if rows < 2 {
			t.Fatalf("rows = %d, want the two seeded hits", rows)
		}
	})
}

func TestExportSearchResultsSARIFForScanFindings(t *testing.T) {
	f := newSearchFixture(t)
	p := f.addProject(t, "Scanner", "scanner-root", nil)
	f.seedRow(t, search.IndexRow{ID: "scan-1", ProjectID: p.ID, Source: search.SourceFinding, HitKind: search.HitKindFinding,
		Kind: "scan", Shape: "artifact", Handle: "opengrep:test", Path: "src/a.go", Line: 4, Trust: "high",
		Snippet: "scan export finding"})

	rec := f.post(t, "/v1/search/export", `{"query":"kind:scan","format":"sarif","origin_project_id":"`+p.ID+`"}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != search.ExportContentType(search.ExportFormatSARIF) {
		t.Fatalf("content type = %q", got)
	}
	var doc struct {
		Runs []struct {
			Results []struct {
				RuleID string `json:"ruleId"`
				Level  string `json:"level"`
			} `json:"results"`
		} `json:"runs"`
	}
	testutil.FailErr(t, "decode sarif", json.Unmarshal(rec.Body.Bytes(), &doc))
	if len(doc.Runs) != 1 || len(doc.Runs[0].Results) != 1 || !strings.Contains(doc.Runs[0].Results[0].RuleID, "opengrep:test") {
		t.Fatalf("sarif = %s", rec.Body.String())
	}

	scope := f.post(t, "/v1/search/export", `{"query":"scan export finding","format":"sarif","origin_project_id":"`+p.ID+`"}`)
	requireErrorCode(t, scope, wire.ApiErrorCodeSearchExportSarifScope)
}

func TestExportSearchResultsRefusals(t *testing.T) {
	f := newSearchFixture(t)
	f.addProject(t, "Refusals", "refusals-root", nil)
	for _, tc := range []struct {
		name string
		body string
		want wire.ApiErrorCode
	}{
		{"malformed body", `[`, wire.ApiErrorCodeInvalidJson},
		{"blank query", `{"query":" ","format":"jsonl"}`, wire.ApiErrorCodeInvalidRequest},
		{"unknown format", `{"query":"x","format":"xml"}`, wire.ApiErrorCodeSearchExportInvalidFormat},
		{"unknown origin", `{"query":"x","format":"csv","origin_project_id":"missing-project"}`, wire.ApiErrorCodeProjectNotFound},
		{"parse error", `{"query":"alpha kind:","format":"csv"}`, wire.ApiErrorCodeSearchQueryInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireErrorCode(t, f.post(t, "/v1/search/export", tc.body), tc.want)
		})
	}
}

func TestSearchHitsToSecurityFindingsMapsTrustAndLocation(t *testing.T) {
	findings := searchHitsToSecurityFindings([]search.Hit{
		{Handle: "rule-a", Path: "a.go", Line: 3, Trust: "critical", Snippet: "a"},
		{HintCode: "hint-b", SourceRef: "ref-b", Trust: "Error"},
		{Handle: "rule-c", Path: "c.go", Trust: "medium"},
		{Handle: "rule-d", Path: "d.go", Trust: "low"},
		{Handle: "rule-e", Path: "e.go", Trust: "unknown"},
	})
	want := []struct {
		rule, uri string
		level     wire.FindingLevel
	}{
		{"rule-a", "a.go", wire.FindingLevelCritical},
		{"hint-b", "ref-b", wire.FindingLevelHigh},
		{"rule-c", "c.go", wire.FindingLevelMedium},
		{"rule-d", "d.go", wire.FindingLevelLow},
		{"rule-e", "e.go", wire.FindingLevelInfo},
	}
	if len(findings) != len(want) {
		t.Fatalf("findings = %d, want %d", len(findings), len(want))
	}
	for i, w := range want {
		got := findings[i]
		if got.RuleID != w.rule || got.Level != w.level || len(got.Locations) != 1 || got.Locations[0].URI != w.uri {
			t.Fatalf("finding %d = %+v, want %+v", i, got, w)
		}
	}
	if findings[0].Locations[0].StartLine != 3 {
		t.Fatalf("start line = %d", findings[0].Locations[0].StartLine)
	}
}
