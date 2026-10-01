package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSourceComparisonViewKeepsFullContentOffMetadata(t *testing.T) {
	_, _, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger)
	p, _, fileID := seedRewrittenReadme(t, srv)
	view := comparisonViewForTest(t, srv, p.ID, wire.SourceComparisonSelector{Scope: &wire.ScopeComparisonSource{Kind: "scope", FileID: fileID, Baseline: "session:s1"}}, "after")
	if view.State != "ready" || view.Comparison == nil || view.Comparison.Summary == nil {
		t.Fatalf("missing comparison: %+v", view)
	}
	encoded, err := json.Marshal(view)
	testutil.FailErr(t, "encode comparison metadata", err)
	if strings.Contains(string(encoded), "third-party") || strings.Contains(string(encoded), `"content":`) {
		t.Fatal("full content in metadata response")
	}
	if content := comparisonTextForTest(t, srv, p.ID, view); content != diffReadmeV3 {
		t.Fatalf("reconstructed content=%q", content)
	}
	current := comparisonViewForTest(t, srv, p.ID, wire.SourceComparisonSelector{Retained: &wire.RetainedComparisonSource{Kind: "retained", ViewID: view.ID, Comparison: "current", RootID: p.Roots[0].ID, Path: "README.md"}}, "before")
	if got := comparisonTextForTest(t, srv, p.ID, current); got != diffReadmeV3 {
		t.Fatalf("current source was not resolved by host: %q", got)
	}
	body := `{"kind":"comparison","operation_id":"` + uuid.NewString() + `","client_id":"test","source":{"kind":"retained","view_id":"` + view.ID + `","comparison":"before","before":"injected"},"intent":{"mode":"changes"}}`
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, newAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/source/views", strings.NewReader(body)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("retained source accepted replacement text: %d %s", response.Code, response.Body.String())
	}
}

func comparisonTextForTest(t *testing.T, srv *Server, projectID string, view wire.SourceComparisonView) string {
	t.Helper()
	if view.State != "ready" {
		t.Fatalf("comparison failed: %+v", view.Failure)
	}
	var text strings.Builder
	for offset := 0; int64(offset) < view.Extent.Rows; {
		frame := comparisonRowsForTest(t, srv, projectID, view, offset)
		for _, row := range frame.Rows {
			text.WriteString(row.Text)
		}
		if frame.Span.End <= int64(offset) {
			t.Fatal("comparison frame did not advance")
		}
		offset = int(frame.Span.End)
	}
	return text.String()
}

func TestSourceViewTextSnapshotPresence(t *testing.T) {
	_, _, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger)
	p, _, _ := seedRewrittenReadme(t, srv)
	empty := ""
	for _, tc := range []struct {
		name                  string
		before, after         *string
		wantBefore, wantAfter string
	}{
		{"empty file", &empty, &empty, "available", "available"},
		{"empty creation", nil, &empty, "absent", "available"},
		{"empty deletion", &empty, nil, "available", "absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := comparisonViewForTest(t, srv, p.ID, wire.SourceComparisonSelector{Text: &wire.TextComparisonSource{Kind: "text", Path: "empty.txt", Before: tc.before, After: tc.after}}, "changes")
			if view.Comparison == nil || view.Comparison.Summary == nil {
				t.Fatalf("missing snapshot summary: %+v", view)
			}
			summary := view.Comparison.Summary
			if summary.Before.Availability != tc.wantBefore || summary.After.Availability != tc.wantAfter {
				t.Fatalf("snapshot presence: before=%s after=%s", summary.Before.Availability, summary.After.Availability)
			}
		})
	}
}
