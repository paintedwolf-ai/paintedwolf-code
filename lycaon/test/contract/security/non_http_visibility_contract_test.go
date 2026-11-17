package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/actioncard"
	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Audit and UI truthfulness for socket/direct external access.

func TestNonHTTPContractTranscriptExternalAccessCannotDisappear(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cases := []struct {
		name       string
		access     *api.ExternalAccess
		wantEgress string
	}{
		{
			name:       "socket_only",
			access:     authzcontext.BuildExternalAccess(authzcontext.ExternalAccessInput{Sockets: []authzcontext.ExternalAccessSocketInput{{ApprovedPath: "/tmp/s.sock", ResolvedPath: "/tmp/s.sock", Scope: "chat"}}}),
			wantEgress: "local_only",
		},
		{
			name:       "direct_ip",
			access:     authzcontext.BuildExternalAccess(authzcontext.ExternalAccessInput{Direct: true}),
			wantEgress: "direct",
		},
		{
			name:       "full_bypass",
			access:     authzcontext.BuildExternalAccess(authzcontext.ExternalAccessInput{FullBypass: true}),
			wantEgress: "direct",
		},
		{
			name:       "unobserved_without_endpoints",
			access:     &api.ExternalAccess{VisibilitySummary: api.ExternalAccessVisibilitySummaryUnobserved},
			wantEgress: "direct",
		},
	}
	for _, tc := range cases {
		desc := actioncard.ForResult(api.ToolResult{Tool: "command", ExternalAccess: tc.access}, nil)
		var found *api.ActionFact
		for i := range desc.Facts {
			if desc.Facts[i].Kind == api.ActionFactNetwork {
				found = &desc.Facts[i]
				break
			}
		}
		if found == nil || found.Egress != tc.wantEgress {
			t.Fatalf("%s: expected network fact with egress %q, got %+v", tc.name, tc.wantEgress, desc.Facts)
		}
	}

	formatSrc := contractcheck.ReadRepoFile(t, root, "lycaon-den/src/chat/action/action-format.ts")
	for _, want := range []string{`"proxy_only"`, `"local_only"`, `"direct"`} {
		if !strings.Contains(formatSrc, want) {
			t.Fatalf("Den action-format.ts must render non-empty text for network egress %s", want)
		}
	}
	cardSrc := contractcheck.ReadRepoFile(t, root, "lycaon-den/src/components/action/ActionCard.tsx")
	if !strings.Contains(cardSrc, "ActionFacts") || !strings.Contains(cardSrc, `data-testid="action-facts"`) {
		t.Fatal("transcript ActionCard must render ActionFacts so non-HTTP external access remains visible")
	}
}

func TestNonHTTPContractMediationUnavailableDistinctFromBypass(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	banners := contractcheck.ReadRepoFile(t, root, "lycaon-den/src/components/shell/SessionProtectionBanners.tsx")
	if !strings.Contains(banners, "MEDIATION_UNAVAILABLE_COPY") || !strings.Contains(banners, "SANDBOX_BYPASS_COPY") {
		t.Fatal("Den must keep distinct mediation-unavailable and sandbox-bypass copy")
	}
	if !strings.Contains(banners, "protection-mediation-unavailable") || !strings.Contains(banners, "protection-sandbox-bypass") {
		t.Fatal("Den must keep distinct protection banner test ids")
	}
	prot := contractcheck.ReadRepoFile(t, root, "lycaon/internal/session/protection/state.go")
	if !strings.Contains(prot, "MediationUnavailable") {
		t.Fatal("session protection state must expose mediation unavailable")
	}
	if !strings.Contains(prot, "SandboxBypass") {
		t.Fatal("session protection state must expose sandbox bypass distinctly")
	}
}
