package discovery

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
)

func TestDiscoveryCallabilityOnlyWhenDeclared(t *testing.T) {
	declared := Profile{RequirePositiveTokenPrice: true}
	priced := discoveryCatalogRecord{ID: "a", InputPrice: new(float64(1))}
	if got := discoveryCallability(priced, declared).State; got != modelinfo.CapabilitySupported {
		t.Fatalf("priced row: got %q, want supported", got)
	}
	unpriced := discoveryCatalogRecord{ID: "b"}
	if got := discoveryCallability(unpriced, declared).State; got != modelinfo.CapabilityUnsupported {
		t.Fatalf("unpriced row: got %q, want unsupported", got)
	}
	// A host that declares nothing about pricing says nothing about callability;
	// an unpriced row there must stay assignable.
	silent := Profile{}
	if got := discoveryCallability(unpriced, silent).State; got != modelinfo.CapabilityUnknown && got != "" {
		t.Fatalf("undeclared profile: got %q, want unknown", got)
	}
}
