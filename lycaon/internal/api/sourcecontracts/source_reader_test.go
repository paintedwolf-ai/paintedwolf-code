package sourcecontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSourceComparisonViewKeepsFullContentOffMetadata(t *testing.T) {
	_, _, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger)
	p, _, fileID := contractfixture.SeedRewrittenReadme(t, srv)
	view := contractfixture.ComparisonViewForTest(t, srv, p.ID, wire.SourceComparisonSelector{Scope: &wire.ScopeComparisonSource{Kind: "scope", FileID: fileID, Baseline: "session:s1"}}, "after")
	if view.State != "ready" || view.Comparison == nil || view.Comparison.Summary == nil {
		t.Fatalf("missing comparison: %+v", view)
	}
	encoded, err := json.Marshal(view)
	testutil.FailErr(t, "encode comparison metadata", err)
	if strings.Contains(string(encoded), "third-party") || strings.Contains(string(encoded), `"content":`) {
		t.Fatal("full content in metadata response")
	}
	if content := contractfixture.ComparisonTextForTest(t, srv, p.ID, view); content != contractfixture.DiffReadmeV3 {
		t.Fatalf("reconstructed content=%q", content)
	}
	current := contractfixture.ComparisonViewForTest(t, srv, p.ID, wire.SourceComparisonSelector{Retained: &wire.RetainedComparisonSource{Kind: "retained", ViewID: view.ID, Comparison: "current", RootID: p.Roots[0].ID, Path: "README.md"}}, "before")
	if got := contractfixture.ComparisonTextForTest(t, srv, p.ID, current); got != contractfixture.DiffReadmeV3 {
		t.Fatalf("current source was not resolved by host: %q", got)
	}
	body := `{"kind":"comparison","operation_id":"` + uuid.NewString() + `","client_id":"test","source":{"kind":"retained","view_id":"` + view.ID + `","comparison":"before","before":"injected"},"intent":{"mode":"changes"}}`
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/"+p.ID+"/source/views", strings.NewReader(body)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("retained source accepted replacement text: %d %s", response.Code, response.Body.String())
	}
}

func TestSourceViewTextSnapshotPresence(t *testing.T) {
	_, _, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger)
	p, _, _ := contractfixture.SeedRewrittenReadme(t, srv)
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
			view := contractfixture.ComparisonViewForTest(t, srv, p.ID, wire.SourceComparisonSelector{Text: &wire.TextComparisonSource{Kind: "text", Path: "empty.txt", Before: tc.before, After: tc.after}}, "changes")
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
