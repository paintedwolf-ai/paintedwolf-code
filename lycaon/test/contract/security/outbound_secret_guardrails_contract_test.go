package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestOutboundSecretGuardrails holds the source laws of the outbound secret
// boundary and its gate's reuse cap. Screening order and the ask verdict are
// behaviour, proven by internal/toolexecution TestOutboundSecretArgvAsksBeforeRun.
func TestOutboundSecretGuardrails(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	assertEveryMatchCarriesAGenericShape(t, root)
	assertOutboundSecretMatchHasNoValue(t, root)
	assertOutboundSecretGitleaksNamespace(t, root)

	if !gate.IsKnown(api.GateSecretOutbound) {
		t.Fatal("the outbound-secret gate must exist")
	}
	if reuse := gate.ReuseFor(api.GateSecretOutbound); reuse.Scope != gate.ScopeProject {
		t.Fatalf("GateSecretOutbound must cap reusable authority at project scope, got %q", reuse.Scope)
	}
	if _, ok := oar.HostDetectors(nil).Lookup("detector://secretmatch").(oar.SecretMatchDetector); !ok {
		t.Fatal("host registry lacks the secret matcher implementation")
	}
}
