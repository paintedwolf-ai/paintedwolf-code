package modelinfo

// CapabilityState distinguishes unknown evidence from unsupported behavior.
type CapabilityState string

const (
	CapabilityUnknown     CapabilityState = "unknown"
	CapabilitySupported   CapabilityState = "supported"
	CapabilityUnsupported CapabilityState = "unsupported"
)

type CapabilityEvidence struct {
	State   CapabilityState `yaml:"state,omitempty" json:"state"`
	Sources []string        `yaml:"sources,omitempty" json:"sources,omitempty"`
}

type ModelCapabilities struct {
	Chat             CapabilityEvidence `yaml:"chat,omitempty" json:"chat"`
	Streaming        CapabilityEvidence `yaml:"streaming,omitempty" json:"streaming"`
	Tools            CapabilityEvidence `yaml:"tools,omitempty" json:"tools"`
	Vision           CapabilityEvidence `yaml:"vision,omitempty" json:"vision"`
	Reasoning        CapabilityEvidence `yaml:"reasoning,omitempty" json:"reasoning"`
	StructuredOutput CapabilityEvidence `yaml:"structured_output,omitempty" json:"structured_output"`
	// PromptCaching says the route caches prompt prefixes on request markers.
	PromptCaching CapabilityEvidence `yaml:"prompt_caching,omitempty" json:"prompt_caching"`
}

func CloneEntries(in []Entry) []Entry {
	out := make([]Entry, len(in))
	for i, model := range in {
		out[i] = model
		out[i].Thinking = model.Thinking.Clone()
		out[i].DiscoveredThinking = model.DiscoveredThinking.Clone()
		if model.Temperature != nil {
			temperature := *model.Temperature
			out[i].Temperature = &temperature
		}
		out[i].Capabilities = CloneCapabilities(model.Capabilities)
		out[i].Callable.Sources = append([]string(nil), model.Callable.Sources...)
	}
	return out
}

func CloneCapabilities(in ModelCapabilities) ModelCapabilities {
	cloneEvidence := func(evidence CapabilityEvidence) CapabilityEvidence {
		evidence.Sources = append([]string(nil), evidence.Sources...)
		return evidence
	}
	in.Chat = cloneEvidence(in.Chat)
	in.Streaming = cloneEvidence(in.Streaming)
	in.Tools = cloneEvidence(in.Tools)
	in.Vision = cloneEvidence(in.Vision)
	in.Reasoning = cloneEvidence(in.Reasoning)
	in.StructuredOutput = cloneEvidence(in.StructuredOutput)
	in.PromptCaching = cloneEvidence(in.PromptCaching)
	return in
}

func Evidence(state CapabilityState, source string) CapabilityEvidence {
	if state == "" {
		state = CapabilityUnknown
	}
	var sources []string
	if source != "" {
		sources = []string{source}
	}
	return CapabilityEvidence{State: state, Sources: sources}
}

func Supported(c CapabilityEvidence) bool { return c.State == CapabilitySupported }

// Refused requires explicit unsupported evidence.
func Refused(c CapabilityEvidence) bool { return c.State == CapabilityUnsupported }

func OptionalState(supported *bool) CapabilityState {
	if supported == nil {
		return CapabilityUnknown
	}
	return State(*supported)
}

func OverlayEvidence(base, overlay CapabilityEvidence) CapabilityEvidence {
	if overlay.State == "" || overlay.State == CapabilityUnknown {
		return base
	}
	if overlay.State == base.State {
		overlay.Sources = MergeSources(base.Sources, overlay.Sources)
	}
	return overlay
}

func MergeSources(left, right []string) []string {
	out := append([]string(nil), left...)
	seen := make(map[string]struct{}, len(out)+len(right))
	for _, source := range out {
		seen[source] = struct{}{}
	}
	for _, source := range right {
		if _, exists := seen[source]; exists {
			continue
		}
		seen[source] = struct{}{}
		out = append(out, source)
	}
	return out
}

func MergeCapabilities(base, overlay ModelCapabilities) ModelCapabilities {
	base.Chat = OverlayEvidence(base.Chat, overlay.Chat)
	base.Streaming = OverlayEvidence(base.Streaming, overlay.Streaming)
	base.Tools = OverlayEvidence(base.Tools, overlay.Tools)
	base.Vision = OverlayEvidence(base.Vision, overlay.Vision)
	base.Reasoning = OverlayEvidence(base.Reasoning, overlay.Reasoning)
	base.StructuredOutput = OverlayEvidence(base.StructuredOutput, overlay.StructuredOutput)
	base.PromptCaching = OverlayEvidence(base.PromptCaching, overlay.PromptCaching)
	return base
}

func (m Entry) EffectiveCapabilities() ModelCapabilities {
	caps := m.Capabilities
	if caps.Chat.State == "" {
		state := CapabilitySupported
		if m.Untyped {
			state = CapabilityUnknown
		}
		caps.Chat = Evidence(state, "model-inventory")
	}
	if caps.Vision.State == "" {
		caps.Vision = Evidence(CapabilityUnknown, "model-inventory")
	}
	return caps
}
func State(supported bool) CapabilityState {
	if supported {
		return CapabilitySupported
	}
	return CapabilityUnsupported
}
