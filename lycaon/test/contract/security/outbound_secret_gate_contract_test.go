package contract

import (
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestDirectionalSecretGateSSOT checks the authorization decision independently.
func TestDirectionalSecretGateSSOT(t *testing.T) {
	t.Parallel()
	assertDirectionalSecretGateSSOT(t, contractcheck.RepoRoot(t))
}

func assertOutboundSecretAskOnly(t *testing.T, root string) {
	t.Helper()
	screen := contractcheck.ReadRepoFile(t, root, "lycaon/internal/webresearch/secret_screen.go")
	for _, want := range []string{
		"SecretDeniedError",
		// Refusals and ask faults use distinct shared codes.
		"toolrejection.OutboundSecretDeniedCode",
		"toolrejection.OutboundSecretScreenFailedCode",
		"SecretScreenFaultError",
		"st.ask",
	} {
		if !strings.Contains(screen, want) {
			t.Fatalf("secret_screen.go missing ask-only polarity marker %q", want)
		}
	}
	if strings.Contains(screen, `"OUTBOUND_SECRET_DENIED"`) {
		t.Fatal("secret_screen.go spells the deny code inline; use toolrejection.OutboundSecretDeniedCode")
	}
	if strings.Contains(screen, "AutoApproved") {
		t.Fatal("webresearch secret screen must not set AutoApproved")
	}
	ask := contractcheck.ReadRepoFile(t, root, "lycaon/internal/toolexecution/outbound_secret_screen.go")
	chunk := sourceFuncChunk(t, ask, "func (e *Secrets) AskSecretScreen")
	// Card answers are resolved after the raise.
	chunk += sourceFuncChunk(t, ask, "func (e *Secrets) resolveSecretScreenDecision")
	payload := contractcheck.ReadRepoFile(t, root, "lycaon/internal/toolsecrets/secret_screen_payload.go")
	if !strings.Contains(chunk, "payload := toolsecrets.SecretReviewPayload(finding, recipients, standingRedaction)") ||
		!strings.Contains(payload, "strings.TrimSpace(finding.GenericShape)") {
		t.Fatal("AskSecretScreen must project GenericShape into the review payload")
	}
	if strings.Contains(chunk+payload, "m.Secret") || strings.Contains(chunk+payload, "Match.Secret") {
		t.Fatal("AskSecretScreen must not read a Secret field")
	}
	assertSecretScreenNeverBlocksWithoutAsking(t, chunk)
}

// sourceFuncChunk returns one function's source, from its declaration to the
// next method declaration.
func sourceFuncChunk(t *testing.T, src, decl string) string {
	t.Helper()
	idx := strings.Index(src, decl)
	if idx < 0 {
		t.Fatalf("%s missing", decl)
	}
	chunk := src[idx:]
	if end := strings.Index(chunk, "\nfunc ("); end > 0 {
		chunk = chunk[:end]
	}
	return chunk
}

// assertSecretScreenNeverBlocksWithoutAsking requires decisions to be reachable
// only after a card is raised.
func assertSecretScreenNeverBlocksWithoutAsking(t *testing.T, chunk string) {
	t.Helper()
	raise := strings.Index(chunk, "raiseAndWaitToolApproval")
	if raise < 0 {
		t.Fatal("AskSecretScreen must raise a card before reporting any blocking outcome")
	}
	for _, decision := range []string{"secretmatch.Unanswered", "secretmatch.Withhold"} {
		if at := strings.Index(chunk, decision); at >= 0 && at < raise {
			t.Fatalf("AskSecretScreen returns %s before raising a card; a decision nobody made is a fault, not an answer", decision)
		}
	}
	if !strings.Contains(chunk, "secretmatch.NewAskFault") {
		t.Fatal("AskSecretScreen must report host failures as secretmatch.NewAskFault")
	}
	// Raise failures retain their cause.
	if !strings.Contains(chunk, "secretmatch.NewAskFault(secretmatch.FaultStageRaise, err)") {
		t.Fatal("AskSecretScreen must carry the raise error into its fault rather than discarding it")
	}
	if !strings.Contains(chunk, "secretmatch.Unanswered") {
		t.Fatal("AskSecretScreen must map an unanswered card to secretmatch.Unanswered")
	}
}

func assertDirectionalSecretGateSSOT(t *testing.T, root string) {
	t.Helper()
	screen := contractcheck.ReadRepoFile(t, root, "lycaon/internal/toolexecution/outbound_secret_screen.go")
	if !strings.Contains(screen, "evaluateSecretScreen(finding, ") {
		t.Fatal("a matcher hit must be evaluated before any card is raised")
	}
	if !strings.Contains(screen, "gate.Evaluate(facts") {
		t.Fatal("the secret screen must decide through the single gate evaluation")
	}
	if strings.Index(screen, "evaluateSecretScreen(finding, ") > strings.Index(screen, "raiseAndWaitToolApproval") {
		t.Fatal("the central gate must decide before the secret screen raises a checkpoint")
	}
	evaluator := contractcheck.ReadRepoFile(t, root, "lycaon/internal/gate/evaluate.go")
	if !strings.Contains(evaluator, "f.Payload.Source == SecretSourcePublicInbound") {
		t.Fatal("directional secret suppression must live in the central gate predicate")
	}
	if !strings.Contains(evaluator, "f.Payload.DestinationTrusted") {
		t.Fatal("trusted-destination suppression must live in the central gate predicate")
	}
	modelScreen := contractcheck.ReadRepoFile(t, root, "lycaon/internal/llm/model_secret_screen.go")
	if !strings.Contains(modelScreen, "gate.SecretSourcePublicInbound") ||
		!strings.Contains(modelScreen, "api.ToolResultOutcomeCompleted") {
		t.Fatal("model screen must produce public-inbound provenance only for a completed native fetch result")
	}
	assertTrustedDestinationIsAFactNotADecision(t, root, modelScreen)
}

// Trust is bound in one place, carried to the alert as a fact, and honored
// only by the gate.
func assertTrustedDestinationIsAFactNotADecision(t *testing.T, root, modelScreen string) {
	t.Helper()
	if !strings.Contains(modelScreen, "DestinationTrusted: destination.Trusted") {
		t.Fatal("model screen must carry destination trust onto the alert")
	}
	if strings.Contains(modelScreen, "if destination.Trusted") || strings.Contains(modelScreen, "destination.Trusted {") {
		t.Fatal("model screen must not branch on destination trust; the gate decides")
	}
	registry := contractcheck.ReadRepoFile(t, root, "lycaon/internal/llm/registry.go")
	if !strings.Contains(registry, "entry.SecretDestinationID()") || !strings.Contains(registry, "entry.SecretScreenTrusted()") {
		t.Fatal("registry must derive the screen destination and its trust from the catalog entry")
	}
	handler := contractcheck.ReadRepoFile(t, root, "lycaon/internal/api/modeladmin/providers.go")
	if !strings.Contains(handler, "resolved.SecretDestinationID()") {
		t.Fatal("the provider update must bind trust to the destination the catalog resolves")
	}
	screen := contractcheck.ReadRepoFile(t, root, "lycaon/internal/toolexecution/outbound_secret_screen.go")
	if !strings.Contains(screen, "DestinationTrusted: finding.DestinationTrusted") {
		t.Fatal("the secret screen must project destination trust into the gate facts")
	}
	if !strings.Contains(screen, "authzledger.ActionSecretDestinationTrusted") {
		t.Fatal("a send authorized by destination trust must reach the ledger")
	}
}
