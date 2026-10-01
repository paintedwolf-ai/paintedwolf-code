package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretspan"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const (
	diffReadmeV0 = "# Demo\n\n## Disclaimer\n\nthird-party data\n"
	diffReadmeV1 = "# Demo\n\n## Quick start\n\nrun it\n\n## Disclaimer\n\nthird-party data\n"
	diffReadmeV2 = "# Demo\n\n## Quick start\n\nrun it\n\n## Disclaimer\n\nthird-party feeds\n"
	diffReadmeV3 = "# Demo\n\n## Quick start\n\nrun it\n\n## Notes\n\nthird-party feeds\n"
)

// seedRewrittenReadme records three README rewrites through the source
// ledger srv was built with (testSourceLedger).
func seedRewrittenReadme(t *testing.T, srv *Server) (wire.Project, string, string) {
	t.Helper()
	ledger := srv.Sources.SourceLedger
	if ledger == nil {
		t.Fatal("seedRewrittenReadme needs a server built with testSourceLedger")
	}
	dir := t.TempDir()
	p := createProjectForTest(t, srv, dir)
	mirrorLedgerProject(t, ledger.LedgerDB(), p)
	testutil.FailErr(t, "write README", os.WriteFile(filepath.Join(dir, "README.md"), []byte(diffReadmeV3), 0o644))

	rootID := p.Roots[0].ID
	base := time.Date(2026, 8, 9, 14, 22, 0, 0, time.UTC)
	for i, step := range [][2]string{
		{diffReadmeV0, diffReadmeV1},
		{diffReadmeV1, diffReadmeV2},
		{diffReadmeV2, diffReadmeV3},
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

func getSourceComparison(t *testing.T, srv *Server, projectID string, q url.Values) (int, wire.SourceComparison) {
	t.Helper()
	req := newAuthedRequest(http.MethodGet,
		"/v1/projects/"+projectID+"/source/comparison?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var out wire.SourceComparison
	if w.Code == http.StatusOK {
		testutil.FailErr(t, "decode comparison", json.Unmarshal(w.Body.Bytes(), &out))
	}
	return w.Code, out
}

func TestRootPrefixInRepoAddressesGitBlobsFromToplevel(t *testing.T) {
	repo := filepath.Join(string(filepath.Separator), "repo")
	root := filepath.Join(repo, "packages", "app")
	if got := sourceapi.RootPrefixInRepo(repo, root) + "src/main.go"; got != "packages/app/src/main.go" {
		t.Fatalf("git blob path = %q", got)
	}
	if got := sourceapi.RootPrefixInRepo(repo, repo) + "src/main.go"; got != "src/main.go" {
		t.Fatalf("toplevel git blob path = %q", got)
	}
}

func TestSourceComparisonIncludesEveryEffectInRange(t *testing.T) {
	_, _, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger)
	p, _, fileID := seedRewrittenReadme(t, srv)

	code, comparison := getSourceComparison(t, srv, p.ID, url.Values{
		"file_id":  {fileID},
		"baseline": {"session:s1"},
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if !comparison.InRange {
		t.Fatal("expected file in range")
	}
	if comparison.Before.Content != diffReadmeV0 {
		t.Fatalf("before = %q, want %q", comparison.Before.Content, diffReadmeV0)
	}
	if strings.Contains(comparison.Before.Content, "Quick start") {
		t.Fatalf("before contains content introduced in range: %q", comparison.Before.Content)
	}
	if comparison.After.Content != diffReadmeV3 {
		t.Fatalf("after = %q, want %q", comparison.After.Content, diffReadmeV3)
	}
}

func TestSourceComparisonScreensEachReadableHistoricalEndpoint(t *testing.T) {
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build secret matcher", err)
	_, _, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger, func(d *Dependencies) { d.SecretSpans = secretspan.New(matcher) })
	p, rootID, fileID := seedRewrittenReadme(t, srv)

	const plantedAWS = "AKIAQYJK5TXV4NZR7SGB"
	after := diffReadmeV3 + "\naws_key = " + plantedAWS + "\n"
	testutil.FailErr(t, "record secret-bearing version", srv.Sources.SourceLedger.Record(
		t.Context(), sourceledger.RecordInput{
			ProjectID: p.ID, RootID: rootID, Path: "README.md",
			Op: wire.SourceChangeOpWrite, Origin: wire.SourceChangeOriginAgent,
			SessionID: "s1", Turn: 2, Before: []byte(diffReadmeV3), After: []byte(after),
		},
	))

	code, comparison := getSourceComparison(t, srv, p.ID, url.Values{
		"file_id": {fileID}, "baseline": {"session:s1"},
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if comparison.Before.SecretScreen == nil {
		t.Fatal("readable before endpoint did not carry a completed screen")
	}
	if comparison.After.SecretScreen == nil {
		t.Fatal("readable after endpoint did not carry a completed screen")
	}
	if comparison.After.SecretScreen.ScreenedRevision != 0 {
		t.Fatalf("immutable content reported editor revision %d",
			comparison.After.SecretScreen.ScreenedRevision)
	}
	found := false
	runes := []rune(comparison.After.Content)
	for _, span := range comparison.After.SecretScreen.Spans {
		if span.Start >= 0 && span.End <= len(runes) &&
			string(runes[span.Start:span.End]) == plantedAWS {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("historical screen did not identify planted credential: %+v",
			comparison.After.SecretScreen.Spans)
	}
}

func TestSourceComparisonRejectsAmbiguousAddressingWithItsReason(t *testing.T) {
	_, _, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger)
	p, _, fileID := seedRewrittenReadme(t, srv)

	for name, q := range map[string]url.Values{
		"both":    {"file_id": {fileID}, "effect_id": {"whatever"}},
		"neither": {"baseline": {"session:s1"}},
	} {
		t.Run(name, func(t *testing.T) {
			req := newAuthedRequest(http.MethodGet,
				"/v1/projects/"+p.ID+"/source/comparison?"+q.Encode(), nil)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", w.Code)
			}
			var resp wire.ErrorResponse
			testutil.FailErr(t, "decode error", json.Unmarshal(w.Body.Bytes(), &resp))
			if resp.Code != "invalid_query" {
				t.Fatalf("code = %q, want invalid_query", resp.Code)
			}
			if param, _ := resp.Details["param"].(string); !strings.Contains(param, "effect_id") {
				t.Fatalf("details = %v; the error must name the parameters at fault", resp.Details)
			}
		})
	}
}

// Foreign and unknown effect IDs are indistinguishable.
func TestSourceComparisonRefusesForeignEffectID(t *testing.T) {
	_, _, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger)
	p, _, _ := seedRewrittenReadme(t, srv)
	other := createProjectForTest(t, srv, t.TempDir())

	res, err := srv.Sources.SourceLedger.QueryWalk(t.Context(), p.ID,
		sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: "s1"}, 10, 0,
		sourceledger.CommitLens{})
	testutil.FailErr(t, "query effects", err)
	if len(res.Files) != 1 {
		t.Fatalf("files = %+v", res.Files)
	}
	effectID := res.Files[0].Effects[0].ID
	code, _ := getSourceComparison(t, srv, other.ID, url.Values{"effect_id": {effectID}})
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
}

func TestReviewedComparisonUsesTheNamedLook(t *testing.T) {
	_, _, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger)
	p, _, fileID := seedRewrittenReadme(t, srv)
	walk, err := srv.Sources.SourceLedger.QueryWalk(t.Context(), p.ID,
		sourceledger.Baseline{Kind: sourceledger.BaselinePresentation}, 10, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "query review effects", err)
	effects := walk.Files[0].Effects
	look := effects[1]
	testutil.FailErr(t, "acknowledge displayed version", srv.Sources.SourceLedger.CompletePresentation(t.Context(), p.ID, fileID, look.ID, look.Ordinal))
	q := url.Values{"file_id": {fileID}, "reviewed_through_ordinal": {strconv.FormatInt(look.Ordinal, 10)}}
	code, comparison := getSourceComparison(t, srv, p.ID, q)
	if code != http.StatusOK || comparison.Before.Content != diffReadmeV0 || comparison.After.Content != diffReadmeV2 {
		t.Fatalf("reviewed response = %d %+v", code, comparison)
	}
	code, unread := getSourceComparison(t, srv, p.ID, url.Values{"file_id": {fileID}})
	if code != http.StatusOK || unread.Before.Content != diffReadmeV2 || unread.After.Content != diffReadmeV3 {
		t.Fatalf("unread response = %d %+v", code, unread)
	}
	code, held := getSourceComparison(t, srv, p.ID, url.Values{
		"file_id": {fileID}, "presentation_after_ordinal": {"0"},
	})
	if code != http.StatusOK || held.Before.Content != diffReadmeV0 || held.After.Content != diffReadmeV3 ||
		held.PresentationAfterOrdinal == nil || *held.PresentationAfterOrdinal != 0 {
		t.Fatalf("held comparison response = %d %+v", code, held)
	}
	latest := effects[0]
	testutil.FailErr(t, "acknowledge latest version", srv.Sources.SourceLedger.CompletePresentation(t.Context(), p.ID, fileID, latest.ID, latest.Ordinal))
	code, _ = getSourceComparison(t, srv, p.ID, q)
	if code != http.StatusConflict {
		t.Fatalf("superseded reviewed response = %d", code)
	}
	code, unread = getSourceComparison(t, srv, p.ID, url.Values{"file_id": {fileID}})
	if code != http.StatusOK || unread.InRange || unread.Before != nil || unread.After != nil {
		t.Fatalf("fully reviewed response = %d %+v", code, unread)
	}
}

func TestReviewedComparisonRejectsInvalidSelectors(t *testing.T) {
	_, _, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger)
	p, _, fileID := seedRewrittenReadme(t, srv)
	for _, q := range []url.Values{
		{"file_id": {fileID}, "reviewed_through_ordinal": {"0"}},
		{"file_id": {fileID}, "reviewed_through_ordinal": {"abc"}},
		{"file_id": {fileID}, "reviewed_through_ordinal": {"1"}, "baseline": {"presentation"}},
		{"file_id": {fileID}, "reviewed_through_ordinal": {"1"}, "mark_user_edits": {"false"}},
		{"effect_id": {"effect"}, "reviewed_through_ordinal": {"1"}},
		{"file_id": {fileID}, "presentation_after_ordinal": {"-1"}},
		{"file_id": {fileID}, "presentation_after_ordinal": {"0"}, "baseline": {"session:s1"}},
		{"file_id": {fileID}, "presentation_after_ordinal": {"0"}, "reviewed_through_ordinal": {"1"}},
	} {
		code, _ := getSourceComparison(t, srv, p.ID, q)
		if code != http.StatusBadRequest {
			t.Fatalf("query %v response = %d", q, code)
		}
	}
}
