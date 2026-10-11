package sourcecontracts

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/secretspan"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestComparisonRefreshesWithdrawnSecretException(t *testing.T) {
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build matcher", err)
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{7}, 32))
	testutil.FailErr(t, "fingerprinter", err)
	matcher.SetFingerprinter(fp)
	server := contractfixture.NewTestServer(t, func(d *hostapi.Dependencies) { d.Approvals.SecretSpans = secretspan.New(matcher) })
	p, err := project.CreateWithRoot(t.Context(), server.Sources.Workspace.ProjectRegistry, t.TempDir())
	testutil.FailErr(t, "create project", err)
	const value = "AKIAQYJK5TXV4NZR7SGB"
	var accepted atomic.Bool
	accepted.Store(true)
	matcher.SetIgnoredSource(func(ctx context.Context) map[secretmatch.SecretFingerprint]bool {
		return map[secretmatch.SecretFingerprint]bool{fp.Fingerprint(value): accepted.Load() && secretmatch.AskAttributionFrom(ctx).ProjectID == p.ID}
	})
	before, after := "old\n", "AWS_ACCESS_KEY_ID="+value+"\n"
	initial := contractfixture.ComparisonViewForTest(t, server, p.ID, wire.SourceComparisonSelector{Text: &wire.TextComparisonSource{
		Kind: "text", Path: "fixture.env", Before: &before, After: &after,
	}}, "full")
	view := contractfixture.AwaitComparisonScreen(t, server, p.ID, initial.ID)
	for _, row := range contractfixture.ComparisonRowsForTest(t, server, p.ID, view, 0).Rows {
		if row.SecretScreen != nil && len(row.SecretScreen.Spans) > 0 {
			t.Fatal("accepted fixture was masked")
		}
	}
	previous := view.ProjectionRevision
	accepted.Store(false)
	testutil.WaitFor(t, 5*time.Second, func() bool {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, contractfixture.NewAuthedRequest(http.MethodGet, sourceapi.SourceViewURL(p.ID, initial.ID), nil))
		view = *contractfixture.ReadSourceViewResponse(t, response, http.StatusOK).Comparison
		return view.ProjectionRevision != previous
	})
	for _, row := range contractfixture.ComparisonRowsForTest(t, server, p.ID, view, 0).Rows {
		if row.SecretScreen != nil && len(row.SecretScreen.Spans) > 0 {
			return
		}
	}
	t.Fatal("withdrawn exception retained its unmasked annotations")
}
