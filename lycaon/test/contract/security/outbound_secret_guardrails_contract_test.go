package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestOutboundSecretGuardrails covers the outbound screening boundary.
func TestOutboundSecretGuardrails(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	assertOutboundSecretSeams(t, root)
	assertOutboundSecretAskOnly(t, root)
	assertEveryMatchCarriesAGenericShape(t, root)
	assertOutboundSecretDetectorRegistered(t, root)
	assertOutboundSecretBoundary(t, root)
	assertOutboundSecretMatchHasNoValue(t, root)
	assertOutboundSecretGitleaksNamespace(t, root)
	assertSecretCatalogSharedWithScanner(t, root)
	assertOutboundSecretRejectAndNotice(t, root)
	assertOutboundSecretDetectorScope(t, root)
	assertDirectionalSecretGateSSOT(t, root)
}

func assertSecretCatalogSharedWithScanner(t *testing.T, root string) {
	t.Helper()
	scanner := readPackageSource(t, root, "lycaon/internal/scan/drivers/library")
	if !strings.Contains(scanner, "secretmatch.BuildScannerProfile") {
		t.Fatal("in-process Gitleaks scanner must compile the shared secret-pattern catalog")
	}
	if !strings.Contains(scanner, "secretmatch.Bundled()") {
		t.Fatal("in-process Gitleaks scanner must compile the bundled secret-pattern layers")
	}
	screen := contractcheck.ReadRepoFile(t, root, "lycaon/internal/app/secret_screen.go")
	if !strings.Contains(screen, "secretmatch.BuildMatcher(secretmatch.Bundled())") {
		t.Fatal("outbound screen must compile the same bundled secret-pattern layers as the scanner")
	}
}

func assertOutboundSecretSeams(t *testing.T, root string) {
	t.Helper()
	type seam struct {
		rel  string
		want string
	}
	seams := []seam{
		{"lycaon/internal/webresearch/rest_provider.go", "screenOutbound"},
		{"lycaon/internal/webresearch/tools.go", "screenOutbound"},
		{"lycaon/internal/webresearch/ssrf.go", "screenOutbound"},
		{"lycaon/internal/oar/secret_detector.go", "Screen"},
	}
	for _, s := range seams {
		body := contractcheck.ReadRepoFile(t, root, s.rel)
		if !strings.Contains(body, s.want) {
			t.Fatalf("%s missing outbound screen call %q", s.rel, s.want)
		}
	}
	mcp := readPackageSource(t, root, "lycaon/internal/mcp")
	for _, want := range []string{"screenCallToolArgs", "ScreenLabeledContext"} {
		if !strings.Contains(mcp, want) {
			t.Fatalf("internal/mcp missing outbound screen call %q", want)
		}
	}
}

// readPackageSource concatenates a package's non-test Go sources.
func readPackageSource(t *testing.T, root, rel string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read package %s: %v", rel, err)
	}
	var b strings.Builder
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b.WriteString(contractcheck.ReadRepoFile(t, root, filepath.Join(rel, name)))
		b.WriteString("\n")
	}
	return b.String()
}

func assertOutboundSecretDetectorRegistered(t *testing.T, root string) {
	t.Helper()
	profile := contractcheck.ReadRepoFile(t, root, "lycaon/config/packs/painted-wolf/platform/host/anchors/oar-profile.yaml")
	if !strings.Contains(profile, "detector://secretmatch") {
		t.Fatal("oar-profile.yaml must list detector://secretmatch")
	}
	build := contractcheck.ReadRepoFile(t, root, "lycaon/internal/app/build_oar.go")
	if !strings.Contains(build, "SecretMatchDetector") {
		t.Fatal("build_oar.go must register SecretMatchDetector")
	}
	det := contractcheck.ReadRepoFile(t, root, "lycaon/internal/oar/secret_detector.go")
	if !strings.Contains(det, `Name() string`) || !strings.Contains(det, `"secretmatch"`) {
		t.Fatal("SecretMatchDetector.Name must return secretmatch")
	}
}

// assertOutboundSecretBoundary checks screening and authority order.
func assertOutboundSecretBoundary(t *testing.T, root string) {
	t.Helper()
	boundary := contractcheck.ReadRepoFile(t, root, "lycaon/internal/toolexecution/executor_impl.go")
	resolveAt := strings.Index(boundary, "e.applyPreInvokeBoundary(ctx, qualifiedName, profileID, args, tc)")
	screenAt := strings.Index(boundary, "e.Secrets.screenArgvSecrets(ctx, qualifiedName, executionArgs, tc)")
	invokeAt := strings.Index(boundary, "e.Metadata.registry.Run(ctx, qualifiedName, executionArgs, tc)")
	if resolveAt < 0 || screenAt < 0 || invokeAt < 0 || !(resolveAt < screenAt && screenAt < invokeAt) {
		t.Fatal("managed references must resolve, then exact values must be screened, before tool invocation")
	}
	preflightSource := contractcheck.ReadRepoFile(t, root, "lycaon/internal/toolexecution/invocation_boundary.go")
	preflight := sourceFuncChunk(t, preflightSource, "func (e *Executor) applyPreInvokeBoundary")
	if !strings.Contains(preflight, "e.Secrets.resolveSecretReferences(ctx, tool, args, tc)") ||
		!strings.Contains(preflight, "tc.Secrets = secrets") {
		t.Fatal("pre-invoke boundary must bind resolved secrets to the invocation context")
	}
	screen := contractcheck.ReadRepoFile(t, root, "lycaon/internal/toolexecution/outbound_secret_screen.go")
	if !strings.Contains(screen, "gate.Evaluate(facts") {
		t.Fatal("the secret screen must decide through the single gate evaluation")
	}
	if !gate.IsKnown(api.GateSecretOutbound) {
		t.Fatal("the outbound-secret gate must exist")
	}
	if reuse := gate.ReuseFor(api.GateSecretOutbound); reuse.Scope != gate.ScopeProject {
		t.Fatalf("GateSecretOutbound must cap reusable authority at project scope, got %q", reuse.Scope)
	}
	offer := contractcheck.ReadRepoFile(t, root, "lycaon/internal/toolexecution/secret_release_offer.go")
	for _, want := range []string{
		"secretmatch.FingerprintDigest", "ApprovalGrantScopeProject",
	} {
		if !strings.Contains(offer, want) {
			t.Fatalf("secret release offer missing exact-authority invariant %q", want)
		}
	}
	plan := contractcheck.ReadRepoFile(t, root, "lycaon/internal/hitl/approval_plan.go")
	if !strings.Contains(plan, "computeRecommendedOptionID") ||
		!strings.Contains(plan, "ApprovalSubjectSecret") ||
		!strings.Contains(plan, "ApprovalRungRedacted") {
		t.Fatal("secret cards must face redacted through computeRecommendedOptionID, not the hour rung")
	}
	lookup := contractcheck.ReadRepoFile(t, root, "lycaon/internal/settings/secret_grant.go")
	if !strings.Contains(lookup, "SecretFingerprintsCovered") {
		t.Fatal("secret release must be re-evaluated through the approval SSOT")
	}
	coverage := contractcheck.ReadRepoFile(t, root, "lycaon/internal/toolexecution/secret_permission.go")
	if !strings.Contains(screen, "e.secretRecipientsCovered(finding, recipients, fingerprintValues)") ||
		!strings.Contains(coverage, ".approvals.approvalGate.SecretFingerprintsCovered") {
		t.Fatal("secret release coverage must use the approval gate")
	}
	approval := contractcheck.ReadRepoFile(t, root, "lycaon/internal/hitl/approval.go")
	if !strings.Contains(approval, "SecretFingerprintsCovered(chatSessionID, projectID, destinationID, surface string, fingerprints []string) bool") {
		t.Fatal("ApprovalGate must require exact secret-release coverage")
	}
}

func assertOutboundSecretRejectAndNotice(t *testing.T, root string) {
	t.Helper()
	for _, rel := range []string{
		"lycaon/config/packs/painted-wolf/platform/policy/OUTBOUND_SECRET_DENIED.yaml",
		"lycaon/config/packs/painted-wolf/platform/host/user-notices/outbound_secret_denied.yaml",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("expected %s: %v", rel, err)
		}
	}
	policy := contractcheck.ReadRepoFile(t, root, "lycaon/config/packs/painted-wolf/platform/policy/OUTBOUND_SECRET_DENIED.yaml")
	if !strings.Contains(policy, "id: OUTBOUND_SECRET_DENIED") {
		t.Fatal("OUTBOUND_SECRET_DENIED policy id mismatch")
	}
	obs := contractcheck.ReadRepoFile(t, root, "lycaon/internal/toolrejection/observation_facts.go")
	if !strings.Contains(obs, `"OUTBOUND_SECRET_DENIED"`) {
		t.Fatal("observation map must include OUTBOUND_SECRET_DENIED")
	}
}

func assertOutboundSecretDetectorScope(t *testing.T, root string) {
	t.Helper()
	// Runtime wiring registers only secret matching.
	build := contractcheck.ReadRepoFile(t, root, "lycaon/internal/app/build_oar.go")
	if !strings.Contains(build, "oar.HostDetectors(matcher)") {
		t.Fatal("startup must install the host detector registry")
	}
	if _, ok := oar.HostDetectors(nil).Lookup("detector://secretmatch").(oar.SecretMatchDetector); !ok {
		t.Fatal("host registry lacks the secret matcher implementation")
	}
	secretDet := contractcheck.ReadRepoFile(t, root, "lycaon/internal/oar/secret_detector.go")
	if !strings.Contains(secretDet, "secret_matches") {
		t.Fatal("SecretMatchDetector must emit secret_matches")
	}
}
