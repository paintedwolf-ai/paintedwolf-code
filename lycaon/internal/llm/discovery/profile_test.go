package discovery

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
)

func TestOpenRouterRequiresPositiveTextOutputMetadata(t *testing.T) {
	profile := OpenRouterProfile()
	if discoveryRecordAllowed(discoveryCatalogRecord{}, profile) {
		t.Fatal("missing output modalities must not become positive chat evidence")
	}
	if !discoveryRecordAllowed(discoveryCatalogRecord{OutputModalities: []string{" TEXT "}}, profile) {
		t.Fatal("text output modality should be matched case-insensitively")
	}
}

func TestOpenRouterTextOnlyInputIsPositiveVisionUnsupportedEvidence(t *testing.T) {
	entry := modelinfo.Entry{}
	applyDiscoveryVision(&entry, discoveryCatalogRecord{InputModalities: []string{"text"}})
	if entry.Capabilities.Vision.State != modelinfo.CapabilityUnsupported {
		t.Fatalf("vision = %+v", entry.Capabilities.Vision)
	}
}

func TestOpenRouterPricePer1K(t *testing.T) {
	in, ok := discoveryPricePer1KFromToken("0.00003")
	if !ok || !floatNear(in, 0.03) {
		t.Fatalf("prompt = %v, ok=%v", in, ok)
	}
	if _, ok := discoveryPricePer1KFromToken(""); ok {
		t.Fatal("expected empty price to fail")
	}
	for _, invalid := range []string{"NaN", "+Inf", "-0.1"} {
		if _, ok := discoveryPricePer1KFromToken(invalid); ok {
			t.Fatalf("invalid price %q was accepted", invalid)
		}
	}
}

func floatNear(a, b float64) bool {
	const eps = 1e-9
	if a > b {
		return a-b <= eps
	}
	return b-a <= eps
}
