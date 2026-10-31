package discovery

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
)

func TestDecodeDiscoveryOpenAIDataRecordsMetadataContextLength(t *testing.T) {
	records, err := decodeDiscoveryOpenAIDataRecords(strings.NewReader(`{
		"object": "list",
		"data": [
			{"id": "deepseek-ai/DeepSeek-V3.2", "metadata": {"context_length": 163840}},
			{"id": "top-level-win", "context_length": 65536, "metadata": {"context_length": 32768}}
		]
	}`))
	if err != nil {
		t.Fatalf("decodeDiscoveryOpenAIDataRecords: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}
	if records[0].ID != "deepseek-ai/DeepSeek-V3.2" || records[0].ContextLength != 163840 {
		t.Fatalf("records[0] = %+v", records[0])
	}
	if records[1].ID != "top-level-win" || records[1].ContextLength != 65536 {
		t.Fatalf("records[1] = %+v", records[1])
	}
}

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
