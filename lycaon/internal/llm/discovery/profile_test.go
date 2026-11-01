package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func TestMistralDiscoveryParsesMetadataAndFiltersNonChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer mistral-test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object": "list",
			"data": [
				{
					"id": "mistral-large-latest",
					"max_context_length": 131072,
					"capabilities": {
						"completion_chat": true,
						"function_calling": true,
						"vision": false
					}
				},
				{
					"id": "pixtral-large-latest",
					"max_context_length": 131072,
					"capabilities": {
						"completion_chat": true,
						"function_calling": true,
						"vision": true
					}
				},
				{
					"id": "mistral-partial-caps",
					"max_context_length": 32768,
					"capabilities": {
						"completion_chat": true
					}
				},
				{
					"id": "mistral-untyped-row",
					"max_context_length": 16384
				},
				{
					"id": "mistral-embed",
					"max_context_length": 8192,
					"capabilities": {
						"completion_chat": false,
						"function_calling": false,
						"vision": false
					}
				}
			]
		}`))
	}))
	defer srv.Close()

	entries, err := FromProfile(context.Background(), srv.URL+"/v1", "mistral-test-key", srv.Client(), MistralProfile())
	if err != nil {
		t.Fatalf("FromProfile(MistralProfile): %v", err)
	}
	byID := make(map[string]modelinfo.Entry, len(entries))
	for _, entry := range entries {
		byID[entry.ID] = entry
	}
	if _, exists := byID["mistral-embed"]; exists {
		t.Fatal("mistral-embed with completion_chat=false must be excluded")
	}

	large, ok := byID["mistral-large-latest"]
	if !ok {
		t.Fatal("missing mistral-large-latest")
	}
	if large.Untyped || large.ContextLength != 131072 {
		t.Fatalf("mistral-large-latest = %+v", large)
	}
	largeCaps := large.EffectiveCapabilities()
	if largeCaps.Chat.State != modelinfo.CapabilitySupported ||
		largeCaps.Tools.State != modelinfo.CapabilitySupported ||
		largeCaps.Vision.State != modelinfo.CapabilityUnsupported ||
		largeCaps.Reasoning.State != modelinfo.CapabilityUnknown {
		t.Fatalf("mistral-large-latest capabilities = %+v", largeCaps)
	}

	pixtral, ok := byID["pixtral-large-latest"]
	if !ok || pixtral.EffectiveCapabilities().Vision.State != modelinfo.CapabilitySupported {
		t.Fatalf("pixtral-large-latest capabilities = %+v", pixtral.EffectiveCapabilities())
	}

	partial, ok := byID["mistral-partial-caps"]
	if !ok {
		t.Fatal("missing mistral-partial-caps")
	}
	partialCaps := partial.EffectiveCapabilities()
	if partialCaps.Chat.State != modelinfo.CapabilitySupported ||
		partialCaps.Tools.State != modelinfo.CapabilityUnknown ||
		partialCaps.Vision.State != modelinfo.CapabilityUnknown {
		t.Fatalf("omitted capabilities must stay unknown: %+v", partialCaps)
	}

	untyped, ok := byID["mistral-untyped-row"]
	if !ok {
		t.Fatal("missing mistral-untyped-row")
	}
	if !untyped.Untyped || untyped.EffectiveCapabilities().Chat.State != modelinfo.CapabilityUnknown {
		t.Fatalf("row without capabilities block must remain untyped/unknown: %+v", untyped)
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

func TestCerebrasProfileMarksChatSupportedAndPreservesUnknownCapabilities(t *testing.T) {
	profile := CerebrasProfile()
	if ProfileUntyped(profile) {
		t.Fatal("CerebrasProfile must be typed (chat-capable)")
	}
	activeTrue := true
	activeFalse := false
	if got := discoveryCallability(discoveryCatalogRecord{ID: "gpt-oss-120b"}, profile); !modelinfo.Supported(got) {
		t.Fatalf("default callability = %+v, want supported", got)
	}
	if got := discoveryCallability(discoveryCatalogRecord{ID: "gpt-oss-120b", Active: &activeTrue}, profile); !modelinfo.Supported(got) {
		t.Fatalf("active=true callability = %+v, want supported", got)
	}
	if got := discoveryCallability(discoveryCatalogRecord{ID: "qwen-3-235b-a22b-instruct-2507", Active: &activeFalse}, profile); !modelinfo.Refused(got) {
		t.Fatalf("active=false callability = %+v, want unsupported", got)
	}
}
