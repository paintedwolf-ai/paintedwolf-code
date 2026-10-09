package contractfixture

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func ComparisonTextForTest(t *testing.T, srv *hostapi.Server, projectID string, view wire.SourceComparisonView) string {
	t.Helper()
	if view.State != "ready" {
		t.Fatalf("comparison failed: %+v", view.Failure)
	}
	var text strings.Builder
	for offset := 0; int64(offset) < view.Extent.Rows; {
		frame := ComparisonRowsForTest(t, srv, projectID, view, offset)
		for _, row := range frame.Rows {
			text.WriteString(row.text)
		}
		if frame.Span.End <= int64(offset) {
			t.Fatal("comparison frame did not advance")
		}
		offset = int(frame.Span.End)
	}
	return text.String()
}

func DigestComparisonsForTest(t *testing.T, srv *hostapi.Server, projectID, body string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, NewAuthedRequest(http.MethodPost, "/v1/projects/"+projectID+"/source/comparison-digests", strings.NewReader(body)))
	return response
}

func GetSourceComparison(t *testing.T, srv *hostapi.Server, projectID string, q url.Values) (int, wire.SourceComparison) {
	t.Helper()
	req := NewAuthedRequest(http.MethodGet,
		"/v1/projects/"+projectID+"/source/comparison?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var out wire.SourceComparison
	if w.Code == http.StatusOK {
		testutil.FailErr(t, "decode comparison", json.Unmarshal(w.Body.Bytes(), &out))
	}
	return w.Code, out
}

func SeedRewrittenReadme(t *testing.T, srv *hostapi.Server) (wire.Project, string, string) {
	t.Helper()
	ledger := srv.Sources.Workspace.SourceLedger
	if ledger == nil {
		t.Fatal("seedRewrittenReadme needs a server built with testSourceLedger")
	}
	dir := t.TempDir()
	p := CreateProjectForTest(t, srv, dir)
	MirrorLedgerProject(t, ledger.LedgerDB(), p)
	testutil.FailErr(t, "write README", os.WriteFile(filepath.Join(dir, "README.md"), []byte(DiffReadmeV3), 0o600))

	rootID := p.Roots[0].ID
	base := time.Date(2026, 8, 9, 14, 22, 0, 0, time.UTC)
	for i, step := range [][2]string{
		{DiffReadmeV0, DiffReadmeV1},
		{DiffReadmeV1, DiffReadmeV2},
		{DiffReadmeV2, DiffReadmeV3},
	} {
		err := ledger.Record(t.Context(), sourceledger.RecordInput{
			ProjectID: p.ID,
			RootID:    rootID, Path: "README.md",
			Op: wire.SourceChangeOpWrite, Origin: wire.SourceChangeOriginAgent,
			SessionID: "s1", Turn: 1,
			Before: []byte(step[0]), After: []byte(step[1]),
			TS: base.Add(time.Duration(i) * time.Minute),
		})
		testutil.FailErr(t, "record README change", err)
	}
	walk, err := ledger.QueryWalk(t.Context(), p.ID,
		sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: "s1"},
		10, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "query README identity", err)
	if len(walk.Files) != 1 {
		t.Fatalf("README walk files = %d, want 1", len(walk.Files))
	}
	return p, rootID, walk.Files[0].FileID
}

const (
	DiffReadmeV0 = "# Demo\n\n## Disclaimer\n\nthird-party data\n"
	DiffReadmeV1 = "# Demo\n\n## Quick start\n\nrun it\n\n## Disclaimer\n\nthird-party data\n"
	DiffReadmeV2 = "# Demo\n\n## Quick start\n\nrun it\n\n## Disclaimer\n\nthird-party feeds\n"
	DiffReadmeV3 = "# Demo\n\n## Quick start\n\nrun it\n\n## Notes\n\nthird-party feeds\n"
)
