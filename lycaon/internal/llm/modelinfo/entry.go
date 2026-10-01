// Package modelinfo defines model inventory evidence and native reasoning controls shared by
// discovery, catalog resolution, and completion adapters.
package modelinfo

import (
	"strings"

	"github.com/lycaon/lycaon/internal/pricing"
)

// Entry describes pricing and request controls for one model.
type Entry struct {
	Thinking             *ThinkingCapabilities `yaml:"thinking,omitempty"`
	DiscoveredThinking   *ThinkingCapabilities `yaml:"-"`
	DiscoveredThinkStyle string                `yaml:"-"`
	ID                   string                `yaml:"id"`
	InputPer1K           float64               `yaml:"input_per_1k"`
	OutputPer1K          float64               `yaml:"output_per_1k"`
	Currency             string                `yaml:"currency"`

	// PriceProvenance records the selected rate source.
	PriceProvenance   PriceProvenance `yaml:"-"`
	DiscoveredPricing *pricing.Rate   `yaml:"-"`

	// Zero-value request controls are omitted.
	Temperature     *float64 `yaml:"temperature,omitempty"`
	MaxTokens       int      `yaml:"max_tokens,omitempty"`
	ReasoningEffort string   `yaml:"reasoning_effort,omitempty"`
	// ReasoningEffortLevels overrides the family mapping for this provider's model.
	ReasoningEffortLevels ReasoningEffortLevels `yaml:"reasoning_effort_levels,omitempty"`
	// Capabilities is the resolved assignment evidence.
	Capabilities ModelCapabilities `yaml:"capabilities,omitempty"`

	// Callable records host-reported model availability.
	Callable CapabilityEvidence `yaml:"callable,omitempty"`

	// PricedAs names the catalog row used for rates.
	PricedAs string `yaml:"priced_as,omitempty"`

	// ContextLength caps the usable token window.
	ContextLength int `yaml:"context_length,omitempty"`

	// Neuron rates support providers that bill in those units.
	InputNeuronsPerM  float64 `yaml:"input_neurons_per_m,omitempty"`
	OutputNeuronsPerM float64 `yaml:"output_neurons_per_m,omitempty"`

	// ThinkingAlwaysOn omits unsupported disable controls.
	ThinkingAlwaysOn bool `yaml:"thinking_always_on,omitempty"`

	// ThinkStyle overrides the family policy for one model.
	ThinkStyle string `yaml:"think_style,omitempty"`

	// Untyped marks discovery rows without capability evidence.
	Untyped bool `yaml:"-"`
}

// PriceProvenance identifies where an Entry's rate fields came from.
type PriceProvenance string

const (
	// PriceProvenanceDiscovered marks rates from a live provider /models (or
	// equivalent) discovery response. Only this provenance feeds ChainPricer tier 1.
	PriceProvenanceDiscovered PriceProvenance = "discovered"
	// PriceProvenanceCatalog marks bundled providers.yaml / models.dev feed hints.
	PriceProvenanceCatalog PriceProvenance = "catalog"
)

// EquivalentID applies provider-defined model equivalence.
func EquivalentID(kind, a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == b {
		return true
	}
	if strings.TrimSpace(kind) != "ollama" {
		return false
	}
	if base, ok := strings.CutSuffix(a, ":latest"); ok && base != "" && base == b {
		return true
	}
	base, ok := strings.CutSuffix(b, ":latest")
	return ok && base != "" && base == a
}

func EntriesFromIDs(ids []string) []Entry {
	out := make([]Entry, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		out = append(out, Entry{ID: id})
	}
	return out
}

// UntypedEntriesFromIDs marks id-only /models rows for fail-closed merge.
func UntypedEntriesFromIDs(ids []string) []Entry {
	out := EntriesFromIDs(ids)
	for i := range out {
		out[i].Untyped = true
	}
	return out
}

func ApplyDiscoveredRate(entry *Entry, rate pricing.Rate) {
	if !rate.Valid() {
		return
	}
	entry.DiscoveredPricing = &rate
	entry.Currency = "USD"
	entry.PriceProvenance = PriceProvenanceDiscovered
	entry.InputPer1K, entry.OutputPer1K = 0, 0
	if rate.InputPer1K != nil {
		entry.InputPer1K = *rate.InputPer1K
	}
	if rate.OutputPer1K != nil {
		entry.OutputPer1K = *rate.OutputPer1K
	}
}

func DiscoveredRateFromEntries(models []Entry, model string) (pricing.Rate, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return pricing.Rate{}, false
	}
	for _, m := range models {
		if !EquivalentID("", m.ID, model) {
			continue
		}
		if m.PriceProvenance != PriceProvenanceDiscovered {
			return pricing.Rate{}, false
		}
		if m.DiscoveredPricing == nil || !m.DiscoveredPricing.Valid() {
			return pricing.Rate{}, false
		}
		return *m.DiscoveredPricing, true
	}
	return pricing.Rate{}, false
}
