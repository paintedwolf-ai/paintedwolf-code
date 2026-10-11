package sourcecontracts

import (
	"net/http"
	"net/url"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
)

func TestSourceAttributionResolvesTheRequestedSessionScope(t *testing.T) {
	_, _, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger)
	p, rootID, _ := contractfixture.SeedRewrittenReadme(t, srv)

	base := url.Values{"path": {"README.md"}, "root_id": {rootID}}
	if code := contractfixture.GetSourceAttribution(t, srv, p.ID, base); code != http.StatusOK {
		t.Fatalf("unscoped attribution status = %d, want 200", code)
	}

	scoped := url.Values{
		"path": {"README.md"}, "root_id": {rootID},
		"session_id": {"00000000-0000-4000-8000-000000000000"},
	}
	if code := contractfixture.GetSourceAttribution(t, srv, p.ID, scoped); code != http.StatusNotFound {
		t.Fatalf("unknown session attribution status = %d, want 404; the "+
			"handler must resolve session scope rather than ignore it", code)
	}
}

// path and root_id address one file; neither is optional.

func TestSourceAttributionRequiresATarget(t *testing.T) {
	_, _, withLedger := contractfixture.TestSourceLedger(t)
	srv := contractfixture.NewTestServer(t, withLedger)
	p, rootID, _ := contractfixture.SeedRewrittenReadme(t, srv)

	for name, q := range map[string]url.Values{
		"no path":    {"root_id": {rootID}},
		"no root_id": {"path": {"README.md"}},
	} {
		t.Run(name, func(t *testing.T) {
			if code := contractfixture.GetSourceAttribution(t, srv, p.ID, q); code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", code)
			}
		})
	}
}
