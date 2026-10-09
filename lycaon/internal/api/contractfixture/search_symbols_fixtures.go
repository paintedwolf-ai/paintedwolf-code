package contractfixture

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func HitsOfKind(resp wire.SearchResponse, kind string) []wire.SearchHit {
	var out []wire.SearchHit
	for _, hit := range resp.Hits {
		if hit.HitKind == kind {
			out = append(out, hit)
		}
	}
	return out
}

const SearchSettlePoll = 200 * time.Millisecond

func SettledSearch(t *testing.T, srv *hostapi.Server, p *project.Project, body map[string]any) wire.SearchResponse {
	t.Helper()
	body["origin_project_id"] = p.ID
	body["budget"] = "complete"
	raw, err := json.Marshal(body)
	testutil.FailErr(t, "encode search", err)
	var resp wire.SearchResponse
	deadline := time.Now().Add(testutil.Timeout(15 * time.Second))
	for {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, NewAuthedRequest(http.MethodPost, "/v1/search", strings.NewReader(string(raw))))
		wait := SearchSettlePoll
		switch w.Code {
		case http.StatusOK:
			testutil.FailErr(t, "decode search", json.Unmarshal(w.Body.Bytes(), &resp))
			if len(resp.Issues) == 0 {
				return resp
			}
		case http.StatusTooManyRequests:
			if seconds, err := strconv.Atoi(w.Header().Get("Retry-After")); err == nil {
				wait = time.Duration(seconds) * time.Second
			}
		default:
			t.Fatalf("search = %d %s", w.Code, w.Body.String())
		}
		if time.Now().Add(wait).After(deadline) {
			t.Fatalf("search never settled: issues %+v", resp.Issues)
		}
		time.Sleep(wait)
	}
}

// searchSettlePoll keeps settledSearch under the host's write limit.

func SymbolSearchProject(t *testing.T, files map[string]string) (*hostapi.Server, *project.Project) {
	t.Helper()
	srv := NewTestServerWithWorkflows(t)
	dir := t.TempDir()
	for name, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(name))
		testutil.FailErr(t, "mkdir "+name, os.MkdirAll(filepath.Dir(abs), 0o750))
		testutil.FailErr(t, "write "+name, os.WriteFile(abs, []byte(content), 0o600))
	}
	p, err := project.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, dir)
	testutil.FailErr(t, "create project", err)
	return srv, p
}

// settledSearch posts a complete-budget search until the answer has no
// coverage issues, so a cold catalog cannot stand in for a real answer. It
// polls within the host's request limit and honours Retry-After.
