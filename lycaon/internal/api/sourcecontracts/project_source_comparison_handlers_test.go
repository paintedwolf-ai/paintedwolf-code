package sourcecontracts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretspan"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

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
	_, _, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger)
	p, _, fileID := contractfixture.SeedRewrittenReadme(t, srv)

	code, comparison := contractfixture.GetSourceComparison(t, srv, p.ID, url.Values{
		"file_id":  {fileID},
		"baseline": {"session:s1"},
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if !comparison.InRange {
		t.Fatal("expected file in range")
	}
	if comparison.Before.Content != contractfixture.DiffReadmeV0 {
		t.Fatalf("before = %q, want %q", comparison.Before.Content, contractfixture.DiffReadmeV0)
	}
	if strings.Contains(comparison.Before.Content, "Quick start") {
		t.Fatalf("before contains content introduced in range: %q", comparison.Before.Content)
	}
	if comparison.After.Content != contractfixture.DiffReadmeV3 {
		t.Fatalf("after = %q, want %q", comparison.After.Content, contractfixture.DiffReadmeV3)
	}
}

func TestSourceComparisonScreensEachReadableHistoricalEndpoint(t *testing.T) {
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build secret matcher", err)
	_, _, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger, func(d *hostapi.Dependencies) { d.Approvals.SecretSpans = secretspan.New(matcher) })
	p, rootID, fileID := contractfixture.SeedRewrittenReadme(t, srv)

	const plantedAWS = "AKIAQYJK5TXV4NZR7SGB"
	after := contractfixture.DiffReadmeV3 + "\naws_key = " + plantedAWS + "\n"
	testutil.FailErr(t, "record secret-bearing version", srv.Sources.Workspace.SourceLedger.Record(
		t.Context(), sourceledger.RecordInput{
			ProjectID: p.ID, RootID: rootID, Path: "README.md",
			Op: wire.SourceChangeOpWrite, Origin: wire.SourceChangeOriginAgent,
			SessionID: "s1", Turn: 2, Before: []byte(contractfixture.DiffReadmeV3), After: []byte(after),
		},
	))

	code, comparison := contractfixture.GetSourceComparison(t, srv, p.ID, url.Values{
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
	_, _, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger)
	p, _, fileID := contractfixture.SeedRewrittenReadme(t, srv)

	for name, q := range map[string]url.Values{
		"both":    {"file_id": {fileID}, "effect_id": {"whatever"}},
		"neither": {"baseline": {"session:s1"}},
	} {
		t.Run(name, func(t *testing.T) {
			req := contractfixture.NewAuthedRequest(http.MethodGet,
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
	_, _, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger)
	p, _, _ := contractfixture.SeedRewrittenReadme(t, srv)
	other := contractfixture.CreateProjectForTest(t, srv, t.TempDir())

	res, err := srv.Sources.Workspace.SourceLedger.Walk.QueryWalk(t.Context(), p.ID,
		sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: "s1"}, 10, 0,
		sourceledger.CommitLens{})
	testutil.FailErr(t, "query effects", err)
	if len(res.Files) != 1 {
		t.Fatalf("files = %+v", res.Files)
	}
	effectID := res.Files[0].Effects[0].ID
	code, _ := contractfixture.GetSourceComparison(t, srv, other.ID, url.Values{"effect_id": {effectID}})
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
}

func TestReviewedComparisonUsesTheNamedLook(t *testing.T) {
	_, _, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger)
	p, _, fileID := contractfixture.SeedRewrittenReadme(t, srv)
	walk, err := srv.Sources.Workspace.SourceLedger.Walk.QueryWalk(t.Context(), p.ID,
		sourceledger.Baseline{Kind: sourceledger.BaselinePresentation}, 10, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "query review effects", err)
	effects := walk.Files[0].Effects
	look := effects[1]
	testutil.FailErr(t, "acknowledge displayed version", srv.Sources.Workspace.SourceLedger.Checkpoints.CompletePresentation(t.Context(), p.ID, fileID, look.ID, look.Ordinal))
	q := url.Values{"file_id": {fileID}, "reviewed_through_ordinal": {strconv.FormatInt(look.Ordinal, 10)}}
	code, comparison := contractfixture.GetSourceComparison(t, srv, p.ID, q)
	if code != http.StatusOK || comparison.Before.Content != contractfixture.DiffReadmeV0 || comparison.After.Content != contractfixture.DiffReadmeV2 {
		t.Fatalf("reviewed response = %d %+v", code, comparison)
	}
	code, unread := contractfixture.GetSourceComparison(t, srv, p.ID, url.Values{"file_id": {fileID}})
	if code != http.StatusOK || unread.Before.Content != contractfixture.DiffReadmeV2 || unread.After.Content != contractfixture.DiffReadmeV3 {
		t.Fatalf("unread response = %d %+v", code, unread)
	}
	code, held := contractfixture.GetSourceComparison(t, srv, p.ID, url.Values{
		"file_id": {fileID}, "presentation_after_ordinal": {"0"},
	})
	if code != http.StatusOK || held.Before.Content != contractfixture.DiffReadmeV0 || held.After.Content != contractfixture.DiffReadmeV3 ||
		held.PresentationAfterOrdinal == nil || *held.PresentationAfterOrdinal != 0 {
		t.Fatalf("held comparison response = %d %+v", code, held)
	}
	latest := effects[0]
	testutil.FailErr(t, "acknowledge latest version", srv.Sources.Workspace.SourceLedger.Checkpoints.CompletePresentation(t.Context(), p.ID, fileID, latest.ID, latest.Ordinal))
	code, _ = contractfixture.GetSourceComparison(t, srv, p.ID, q)
	if code != http.StatusConflict {
		t.Fatalf("superseded reviewed response = %d", code)
	}
	code, unread = contractfixture.GetSourceComparison(t, srv, p.ID, url.Values{"file_id": {fileID}})
	if code != http.StatusOK || unread.InRange || unread.Before != nil || unread.After != nil {
		t.Fatalf("fully reviewed response = %d %+v", code, unread)
	}
}

func TestReviewedComparisonRejectsInvalidSelectors(t *testing.T) {
	_, _, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger)
	p, _, fileID := contractfixture.SeedRewrittenReadme(t, srv)
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
		code, _ := contractfixture.GetSourceComparison(t, srv, p.ID, q)
		if code != http.StatusBadRequest {
			t.Fatalf("query %v response = %d", q, code)
		}
	}
}
