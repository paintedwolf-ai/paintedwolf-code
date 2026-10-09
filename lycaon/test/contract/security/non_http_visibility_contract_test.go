package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Audit and UI truthfulness for socket/direct external access.

func TestNonHTTPContractMandatoryAuthzDetailForSocketAndDirect(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	actions := map[authzcontext.EventAction]bool{}
	for _, a := range authzcontext.AllEventActions() {
		actions[a] = true
	}
	for _, want := range []authzcontext.EventAction{
		authzcontext.EventActionCapabilityApplied,
		authzcontext.EventActionCapabilityGranted,
		authzcontext.EventActionDirectIPStarted,
		authzcontext.EventActionDirectIPCompleted,
		authzcontext.EventActionDetectionResolved,
	} {
		if !actions[want] {
			t.Fatalf("missing mandatory authz action %q", want)
		}
	}

	sock := contractcheck.ReadRepoFile(t, root, "lycaon/internal/tools/socket_authorization_ledger.go")
	if !strings.Contains(sock, "recordSocketCapabilityApplied") || !strings.Contains(sock, "authzledger.ActionCapabilityApplied") {
		t.Fatal("applied sockets must emit capability_applied records")
	}
	direct := contractcheck.ReadRepoFile(t, root, "lycaon/internal/toolexecution/direct_ip_capability.go")
	if !strings.Contains(direct, "emitDirectIPLifecycle") || !strings.Contains(direct, "DirectIPLifecycleStarted") {
		t.Fatal("direct execution must emit lifecycle authz detail")
	}
	adapter := contractcheck.ReadRepoFile(t, root, "lycaon/internal/authzcontext/recorder_adapter.go")
	if !strings.Contains(adapter, "BuildExternalAccess") {
		t.Fatal("authz recorder must build ExternalAccess detail via BuildExternalAccess")
	}
}

func TestNonHTTPContractExternalAccessBoundsRedactionAndUnknown(t *testing.T) {
	t.Parallel()
	sockets := make([]authzcontext.ExternalAccessSocketInput, 0, authzcontext.MaxExternalAccessSockets+4)
	for i := 0; i < authzcontext.MaxExternalAccessSockets+4; i++ {
		sockets = append(sockets, authzcontext.ExternalAccessSocketInput{
			ApprovedPath: "/very/long/path/that/should/be/redacted/or/capped/" + strings.Repeat("x", 40) + string(rune('a'+i%26)) + ".sock",
			ResolvedPath: "/resolved/" + strings.Repeat("y", 40) + string(rune('a'+i%26)) + ".sock",
			Scope:        "chat",
		})
	}
	ea := authzcontext.BuildExternalAccess(authzcontext.ExternalAccessInput{Sockets: sockets})
	if len(ea.Sockets) > authzcontext.MaxExternalAccessSockets {
		t.Fatalf("sockets must be capped at %d, got %d", authzcontext.MaxExternalAccessSockets, len(ea.Sockets))
	}

	empty := authzcontext.BuildExternalAccess(authzcontext.ExternalAccessInput{})
	if empty.VisibilitySummary != api.ExternalAccessVisibilitySummaryUnknown {
		t.Fatalf("missing detail must be unknown, got %q", empty.VisibilitySummary)
	}
	none := authzcontext.BuildExternalAccess(authzcontext.ExternalAccessInput{AffirmativeNone: true})
	if none.VisibilitySummary != api.ExternalAccessVisibilitySummaryNone {
		t.Fatalf("affirmative none must be none, got %q", none.VisibilitySummary)
	}
	malformed := authzcontext.BuildExternalAccess(authzcontext.ExternalAccessInput{Malformed: true})
	if malformed.VisibilitySummary != api.ExternalAccessVisibilitySummaryUnknown {
		t.Fatalf("malformed detail must be unknown, got %q", malformed.VisibilitySummary)
	}
}

func TestNonHTTPContractDeclaredOnlyInDeclaredFieldsDirectUnobserved(t *testing.T) {
	t.Parallel()
	ea := authzcontext.BuildExternalAccess(authzcontext.ExternalAccessInput{
		Direct:               true,
		DeclaredDestinations: []string{"198.51.100.9:443", "example.test:22"},
	})
	if len(ea.Endpoints) != 0 {
		t.Fatalf("declared must not populate observed endpoints: %+v", ea.Endpoints)
	}
	if len(ea.DeclaredDestinations) != 2 {
		t.Fatalf("declared destinations missing: %+v", ea.DeclaredDestinations)
	}
	if ea.Direct == nil || ea.Direct.ActualDestinationVisibility != api.ExternalAccessVisibilityUnobserved {
		t.Fatalf("direct must mark unobserved destinations: %+v", ea.Direct)
	}
	hasDirectMode := false
	for _, m := range ea.Modes {
		if m == api.ExternalAccessModeDirectIP {
			hasDirectMode = true
		}
	}
	if !hasDirectMode {
		t.Fatal("direct execution must derive direct_ip mode")
	}
}

func TestNonHTTPContractNoDockerDaemonInnerEffectSynthesis(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	eaSrc := contractcheck.ReadRepoFile(t, root, "lycaon/internal/authzcontext/external_access.go")
	for _, banned := range []string{"docker.sock", "containerd", "synthesizeInner", "daemonEndpoint"} {
		if strings.Contains(eaSrc, banned) {
			t.Fatalf("BuildExternalAccess must not synthesize Docker/daemon inner effects (%s)", banned)
		}
	}
	ea := authzcontext.BuildExternalAccess(authzcontext.ExternalAccessInput{
		Sockets: []authzcontext.ExternalAccessSocketInput{{
			ApprovedPath: "/var/run/docker.sock",
			ResolvedPath: "/var/run/docker.sock",
			Scope:        "chat",
		}},
	})
	if len(ea.Endpoints) != 0 {
		t.Fatalf("socket authority must not invent mediated endpoints: %+v", ea.Endpoints)
	}
	if len(ea.Sockets) != 1 {
		t.Fatalf("outer socket row only, got %+v", ea.Sockets)
	}
}

func TestNonHTTPContractTranscriptExternalAccessCannotDisappear(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	pres := contractcheck.ReadRepoFile(t, root, "lycaon-den/src/chat/tool/external-access-presentation.ts")
	if !strings.Contains(pres, "mustShow") {
		t.Fatal("Den presentation must compute mustShow for exceptional access")
	}
	if !strings.Contains(pres, "direct_ip") || !strings.Contains(pres, "full_bypass") || !strings.Contains(pres, "unobserved") {
		t.Fatal("Den presentation must keep socket/direct/bypass/unobserved visible")
	}
	chicklet := contractcheck.ReadRepoFile(t, root, "lycaon-den/src/components/tool/NetworkChicklet.tsx")
	if !strings.Contains(chicklet, "buildExternalAccessView") && !strings.Contains(chicklet, "EXTERNAL_ACCESS_LABEL") {
		// NetworkChicklet may import the presentation helper under a different local name.
		if !strings.Contains(chicklet, "external-access-presentation") && !strings.Contains(chicklet, "External access") {
			t.Fatal("transcript External access chicklet must remain wired")
		}
	}
}

func TestNonHTTPContractUnobservedAndUnknownStayDistinct(t *testing.T) {
	t.Parallel()
	ea := authzcontext.BuildExternalAccess(authzcontext.ExternalAccessInput{Direct: true})
	if ea.VisibilitySummary != api.ExternalAccessVisibilitySummaryUnobserved {
		t.Fatalf("direct-only visibility=%q", ea.VisibilitySummary)
	}
	unknown := authzcontext.BuildExternalAccess(authzcontext.ExternalAccessInput{})
	if unknown.VisibilitySummary != api.ExternalAccessVisibilitySummaryUnknown {
		t.Fatalf("empty visibility=%q", unknown.VisibilitySummary)
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
	prot := contractcheck.ReadRepoFile(t, root, "lycaon/internal/session/protection_state.go")
	if !strings.Contains(prot, "MediationUnavailable") {
		t.Fatal("session protection state must expose mediation unavailable")
	}
	if !strings.Contains(prot, "SandboxBypass") {
		t.Fatal("session protection state must expose sandbox bypass distinctly")
	}
}

func TestNonHTTPContractSettingsRevocationWarnsAlreadyRunning(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	if api.SocketGrantRevokeAppliesTo == "" || !strings.Contains(api.SocketGrantRevokeAppliesTo, "Already-running") {
		t.Fatal("SocketGrantRevokeAppliesTo must warn about already-running processes")
	}
	mapSrc := contractcheck.ReadRepoFile(t, root, "lycaon/internal/approvals/approval_grants.go")
	if !strings.Contains(mapSrc, "SocketGrantRevokeAppliesTo") {
		t.Fatal("Settings grant mapping must surface revoke_applies_to")
	}
	denCopy := contractcheck.ReadRepoFile(t, root, "lycaon-den/src/settings/security/saved-approvals-copy.ts")
	if !strings.Contains(strings.ToLower(denCopy), "already running") {
		t.Fatal("Den Settings must warn revoke applies to next process only")
	}
}
