package sourcecontracts

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSourceComparisonDigestMeasuresWhatAViewPresents(t *testing.T) {
	_, _, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger)
	p, _, fileID := contractfixture.SeedRewrittenReadme(t, srv)
	scope := wire.SourceComparisonSelector{Scope: &wire.ScopeComparisonSource{Kind: "scope", FileID: fileID, Baseline: "session:s1"}}
	missing := wire.SourceComparisonSelector{Effect: &wire.EffectComparisonSource{Kind: "effect", EffectID: uuid.NewString()}}
	encoded, err := json.Marshal(wire.SourceComparisonDigestRequest{Sources: []wire.SourceComparisonSelector{scope, missing}})
	testutil.FailErr(t, "encode digest request", err)

	response := contractfixture.DigestComparisonsForTest(t, srv, p.ID, string(encoded))
	if response.Code != http.StatusOK {
		t.Fatalf("digest: status=%d body=%s", response.Code, response.Body.String())
	}
	var digests wire.SourceComparisonDigests
	testutil.FailErr(t, "decode digests", json.Unmarshal(response.Body.Bytes(), &digests))
	if len(digests.Digests) != 2 {
		t.Fatalf("digests=%+v", digests)
	}
	measured := digests.Digests[0]
	if !measured.InRange || measured.Summary == nil || measured.Failure != nil {
		t.Fatalf("scope digest=%+v", measured)
	}
	// A failed source answers in its own slot.
	if failed := digests.Digests[1]; failed.Failure == nil || failed.Summary != nil {
		t.Fatalf("missing effect digest=%+v", failed)
	}

	view := contractfixture.ComparisonViewForTest(t, srv, p.ID, scope, "changes")
	if view.State != "ready" || view.Comparison == nil || view.Comparison.Summary == nil {
		t.Fatalf("view=%+v", view)
	}
	if got, want := measured.Summary.Added, view.Comparison.Summary.Added; got != want {
		t.Fatalf("digest added=%d, view added=%d", got, want)
	}
	if got, want := measured.Summary.Removed, view.Comparison.Summary.Removed; got != want {
		t.Fatalf("digest removed=%d, view removed=%d", got, want)
	}
	if int64(measured.ChangesRows) != view.Extent.Rows {
		t.Fatalf("digest changes_rows=%d, view extent=%d", measured.ChangesRows, view.Extent.Rows)
	}
}

func TestSourceComparisonDigestRejectsSourcesAViewMustRead(t *testing.T) {
	_, _, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger)
	p, _, _ := contractfixture.SeedRewrittenReadme(t, srv)
	for name, body := range map[string]string{
		"empty":    `{"sources":[]}`,
		"retained": `{"sources":[{"kind":"retained","view_id":"` + uuid.NewString() + `","comparison":"before"}]}`,
		"text":     `{"sources":[{"kind":"text","path":"a.txt","before":"a","after":"b"}]}`,
		"session":  `{"session_id":"not-a-uuid","sources":[{"kind":"effect","effect_id":"` + uuid.NewString() + `"}]}`,
	} {
		if response := contractfixture.DigestComparisonsForTest(t, srv, p.ID, body); response.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d body=%s", name, response.Code, response.Body.String())
		}
	}
	sources := strings.Repeat(`{"kind":"effect","effect_id":"`+uuid.NewString()+`"},`, 65)
	if response := contractfixture.DigestComparisonsForTest(t, srv, p.ID, `{"sources":[`+strings.TrimSuffix(sources, ",")+`]}`); response.Code != http.StatusBadRequest {
		t.Fatalf("oversized batch: status=%d", response.Code)
	}
}
