package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

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
	server := newTestServer(t, func(d *Dependencies) { d.SecretSpans = secretspan.New(matcher) })
	p, err := project.CreateWithRoot(t.Context(), server.projectRegistry, t.TempDir())
	testutil.FailErr(t, "create project", err)
	const value = "AKIAQYJK5TXV4NZR7SGB"
	var accepted atomic.Bool
	accepted.Store(true)
	matcher.SetIgnoredSource(func(ctx context.Context) map[secretmatch.SecretFingerprint]bool {
		return map[secretmatch.SecretFingerprint]bool{fp.Fingerprint(value): accepted.Load() && secretmatch.AskAttributionFrom(ctx).ProjectID == p.ID}
	})
	before, after := "old\n", "AWS_ACCESS_KEY_ID="+value+"\n"
	initial := comparisonViewForTest(t, server, p.ID, wire.SourceComparisonSelector{Text: &wire.TextComparisonSource{
		Kind: "text", Path: "fixture.env", Before: &before, After: &after,
	}}, "full")
	view := awaitComparisonScreen(t, server, p.ID, initial.ID)
	for _, row := range comparisonRowsForTest(t, server, p.ID, view, 0).Rows {
		if row.SecretScreen != nil && len(row.SecretScreen.Spans) > 0 {
			t.Fatal("accepted fixture was masked")
		}
	}
	previous := view.ProjectionRevision
	accepted.Store(false)
	testutil.WaitFor(t, 5*time.Second, func() bool {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, newAuthedRequest(http.MethodGet, sourceapi.SourceViewURL(p.ID, initial.ID), nil))
		view = *readSourceViewResponse(t, response, http.StatusOK).Comparison
		return view.ProjectionRevision != previous
	})
	for _, row := range comparisonRowsForTest(t, server, p.ID, view, 0).Rows {
		if row.SecretScreen != nil && len(row.SecretScreen.Spans) > 0 {
			return
		}
	}
	t.Fatal("withdrawn exception retained its unmasked annotations")
}
