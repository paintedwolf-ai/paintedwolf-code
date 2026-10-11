package searchadmin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/symbolsearch"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Each call crosses HTTP and constructs the service through the real handler.
// A warm source catalog alone must not masquerade as retained query progress.
func TestSearchProgressSurvivesHTTPAndBelongsToHandler(t *testing.T) {
	f := newSearchFixture(t)
	files := map[string]string{}
	for i := range symbolsearch.OutlineFileCap*2 + 3 {
		files[fmt.Sprintf("f%03d.go", i)] = "package p\nfunc Target() {}\n"
	}
	p := f.addProject(t, "bounded", "source", files)
	body := jsonBody(t, map[string]any{"query": "kind:symbol Target", "origin_project_id": p.ID, "budget": "complete", "limit": 500})
	server := httptest.NewServer(f.router)
	defer server.Close()
	first := requestSearchHTTP(t, server, body)
	if len(first.Hits) >= len(files) || !hasSearchIssue(first, "symbol_pending") {
		t.Fatalf("fixture did not exercise resumable outlining: hits=%d issues=%+v", len(first.Hits), first.Issues)
	}
	previous := len(first.Hits)
	result := first
	for attempt := 0; attempt < 8 && hasSearchIssue(result, "symbol_pending"); attempt++ {
		result = requestSearchHTTP(t, server, body)
		if len(result.Hits) < previous {
			t.Fatalf("unchanged HTTP search lost progress: %d -> %d", previous, len(result.Hits))
		}
		previous = len(result.Hits)
	}
	if hasSearchIssue(result, "symbol_pending") || len(result.Hits) != len(files) || !result.Exhaustive {
		t.Fatalf("bounded HTTP requests did not complete: hits=%d want=%d issues=%+v", len(result.Hits), len(files), result.Issues)
	}
	fresh := New(&httpio.Responder{}, Dependencies{Database: f.database, Projects: f.projects})
	independent := httptest.NewServer(f.mount(fresh))
	defer independent.Close()
	restarted := requestSearchHTTP(t, independent, body)
	if len(restarted.Hits) >= len(files) || !hasSearchIssue(restarted, "symbol_pending") {
		t.Fatalf("new handler inherited completed query state: hits=%d issues=%+v", len(restarted.Hits), restarted.Issues)
	}
}

func requestSearchHTTP(t *testing.T, server *httptest.Server, body string) wire.SearchResponse {
	t.Helper()
	response, err := server.Client().Post(server.URL+"/v1/search", "application/json", strings.NewReader(body))
	testutil.FailErr(t, "send search HTTP request", err)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("search HTTP status=%d", response.StatusCode)
	}
	var result wire.SearchResponse
	testutil.FailErr(t, "decode search HTTP response", json.NewDecoder(response.Body).Decode(&result))
	return result
}

func hasSearchIssue(result wire.SearchResponse, reason string) bool {
	for _, issue := range result.Issues {
		if string(issue.Reason) == reason {
			return true
		}
	}
	return false
}

func TestSearchHTTPFederationStopsAtExplicitRetentionBound(t *testing.T) {
	f := newSearchFixture(t)
	files := map[string]string{}
	for i := range symbolsearch.OutlineFileCap + 1 {
		files[fmt.Sprintf("f%03d.go", i)] = "package p\nfunc Target() {}\n"
	}
	origin := ""
	// The implementation retains 16 project frontiers. The seventeenth must
	// declare a terminal capacity bound instead of offering an endless retry.
	for i := range 17 {
		p := f.addProject(t, fmt.Sprintf("project-%02d", i), fmt.Sprintf("root-%02d", i), files)
		if i == 0 {
			origin = p.ID
		}
	}
	server := httptest.NewServer(f.router)
	defer server.Close()
	body := jsonBody(t, map[string]any{"query": "kind:symbol Target", "origin_project_id": origin, "budget": "complete", "limit": 500})
	var result wire.SearchResponse
	for attempt := range 16 {
		result = requestSearchHTTP(t, server, body)
		if hasSearchIssue(result, "executor_error") {
			t.Fatalf("HTTP federation failed: %+v", result.Issues)
		}
		if !hasSearchIssue(result, "symbol_pending") {
			break
		}
		if attempt == 15 {
			t.Fatalf("HTTP federation endlessly advertised lost progress: %+v", result.Issues)
		}
	}
	terminal := 0
	for _, issue := range result.Issues {
		if string(issue.Reason) == "symbol_budget" {
			if issue.Limit != 16 {
				t.Fatalf("terminal bound lost its project units: %+v", issue)
			}
			terminal++
		}
	}
	if terminal != 1 || result.Exhaustive {
		t.Fatalf("overflow coverage=%+v exhaustive=%v", result.Issues, result.Exhaustive)
	}
}
