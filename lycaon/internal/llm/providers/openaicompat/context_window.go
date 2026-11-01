package openaicompat

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
)

const localContextProbeTimeout = 3 * time.Second

type servingContextFact struct {
	ContextLength int
	Source        string
	Probed        bool
	Overflow      bool
}

type servingContextState struct {
	mu       sync.Mutex
	identity string
	byModel  map[string]servingContextFact
}

func newServingContextState() *servingContextState {
	return &servingContextState{
		byModel: make(map[string]servingContextFact),
	}
}

func stampDeclaredContextSource(models []modelinfo.Entry) []modelinfo.Entry {
	if len(models) == 0 {
		return nil
	}
	out := modelinfo.CloneEntries(models)
	for i := range out {
		if out[i].ContextLength > 0 && out[i].ContextLengthSource == "" {
			out[i].ContextLengthSource = "config"
		}
	}
	return out
}

func (p *Provider) endpointIdentity() string {
	if p == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(p.baseURL), "/") + "\x00" + strings.TrimSpace(p.apiKey)
}

func (p *Provider) ensureContextStateLocked() *servingContextState {
	if p.contextState == nil {
		p.contextState = newServingContextState()
	}
	id := p.endpointIdentity()
	if p.contextState.identity != id {
		p.contextState.identity = id
		p.contextState.byModel = make(map[string]servingContextFact)
	}
	return p.contextState
}

// WithEndpoint updates the provider's base URL and API key, refreshing any
// cached serving context window when the endpoint identity changes.
func (p *Provider) WithEndpoint(baseURL, apiKey string) *Provider {
	p.baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	p.apiKey = apiKey
	if p.contextState != nil {
		p.contextState.mu.Lock()
		_ = p.ensureContextStateLocked()
		p.contextState.mu.Unlock()
	}
	return p
}

// ModelEntry returns the catalog model entry enriched with any discovered or
// overflow-reported serving context window. A declared context_length wins over
// discovery, while a smaller overflow-reported window caps the live entry.
func (p *Provider) ModelEntry(model string) (modelinfo.Entry, bool) {
	model = strings.TrimSpace(model)
	entry, ok := p.Catalog.ModelEntry(model)
	if ok && entry.ContextLength > 0 && entry.ContextLengthSource == "" {
		entry.ContextLengthSource = "config"
	}
	fact, hasFact := p.lookupContextFact(model)
	if ok && entry.ContextLength > 0 {
		if hasFact && fact.Overflow && fact.ContextLength > 0 && fact.ContextLength < entry.ContextLength {
			entry.ContextLength = fact.ContextLength
			entry.ContextLengthSource = fact.Source
		}
		return entry, true
	}
	if hasFact && fact.ContextLength > 0 {
		if !ok {
			entry = modelinfo.Entry{ID: model}
		}
		entry.ContextLength = fact.ContextLength
		entry.ContextLengthSource = fact.Source
		return entry, true
	}
	return entry, ok
}

func (p *Provider) lookupContextFact(model string) (servingContextFact, bool) {
	if p == nil || p.contextState == nil {
		return servingContextFact{}, false
	}
	model = strings.TrimSpace(model)
	p.contextState.mu.Lock()
	defer p.contextState.mu.Unlock()
	st := p.ensureContextStateLocked()
	if model != "" {
		if fact, ok := st.byModel[model]; ok {
			return fact, true
		}
	}
	if fact, ok := st.byModel["*"]; ok {
		return fact, true
	}
	return servingContextFact{}, false
}

// RecordContextWindow records an observed serving context window (such as from
// a structured context-overflow 400 error) for model on the current endpoint.
func (p *Provider) RecordContextWindow(model string, window int, source string, overflow bool) {
	if p == nil || window <= 0 {
		return
	}
	if p.contextState == nil {
		p.contextState = newServingContextState()
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	if source == "" {
		source = "provider-overflow"
	}
	p.contextState.mu.Lock()
	defer p.contextState.mu.Unlock()
	st := p.ensureContextStateLocked()
	fact := servingContextFact{
		ContextLength: window,
		Source:        source,
		Probed:        true,
		Overflow:      overflow,
	}
	st.byModel[model] = fact
}

// ContextLimit returns the declared, recorded, or probed serving context window
// for model. A declared context_length on the model entry wins over discovery.
func (p *Provider) ContextLimit(ctx context.Context, model string) int {
	if p == nil {
		return 0
	}
	model = strings.TrimSpace(model)
	if entry, ok := p.ModelEntry(model); ok && entry.ContextLength > 0 {
		return entry.ContextLength
	}
	if fact, ok := p.lookupContextFact(model); ok && fact.Probed {
		return fact.ContextLength
	}
	window, source := p.fetchServingContext(ctx, model)
	if p.contextState == nil {
		p.contextState = newServingContextState()
	}
	p.contextState.mu.Lock()
	st := p.ensureContextStateLocked()
	fact := servingContextFact{
		ContextLength: window,
		Source:        source,
		Probed:        true,
	}
	if model != "" {
		st.byModel[model] = fact
	}
	if source == discovery.LlamaCppSource && window > 0 {
		st.byModel["*"] = fact
	}
	p.contextState.mu.Unlock()
	return window
}

// ContextLength implements llm.ContextLengthLookup for direct provider queries.
func (p *Provider) ContextLength(_, model string) int {
	return p.ContextLimit(context.Background(), model)
}

// IsLocalProvider reports whether this provider is configured for local inference.
func (p *Provider) IsLocalProvider(_ string) bool {
	return p != nil && p.profile.Local
}

func (p *Provider) fetchServingContext(ctx context.Context, model string) (int, string) {
	if p == nil || strings.TrimSpace(p.baseURL) == "" {
		return 0, ""
	}
	probeCtx, cancel := providerwire.ProbeContext(ctx, localContextProbeTimeout)
	defer cancel()

	var (
		discovered []modelinfo.Entry
		err        error
	)
	switch p.profile.Discovery {
	case providerprofile.DiscoveryLMStudio:
		discovered, err = discovery.LMStudioModels(probeCtx, p.baseURL, p.apiKey, p.streamClient)
	case providerprofile.DiscoveryOMLX:
		discovered, err = discovery.OMLXModels(probeCtx, p.baseURL, p.apiKey, p.streamClient)
	case providerprofile.DiscoveryLiteLLM:
		discovered, err = discovery.LiteLLMModels(probeCtx, p.baseURL, p.apiKey, p.streamClient)
	case providerprofile.DiscoveryConfigured, "":
		prof := p.profile.DiscoveryProfile
		prof.ProbeProps = true
		discovered, err = discovery.FromProfile(probeCtx, p.baseURL, p.apiKey, p.streamClient, prof)
	default:
		return 0, ""
	}
	if err == nil && len(discovered) > 0 {
		if p.contextState != nil {
			p.contextState.mu.Lock()
			st := p.ensureContextStateLocked()
			for _, d := range discovered {
				if d.ContextLength > 0 && strings.TrimSpace(d.ID) != "" {
					st.byModel[strings.TrimSpace(d.ID)] = servingContextFact{
						ContextLength: d.ContextLength,
						Source:        d.ContextLengthSource,
						Probed:        true,
					}
				}
			}
			p.contextState.mu.Unlock()
		}
		for _, d := range discovered {
			if d.ContextLength > 0 && (model == "" || d.ID == "*" || modelinfo.EquivalentID("", d.ID, model)) {
				return d.ContextLength, d.ContextLengthSource
			}
		}
	}
	if nCtx := discovery.ProbeLlamaCppProps(probeCtx, p.baseURL, p.apiKey, p.streamClient); nCtx > 0 {
		return nCtx, discovery.LlamaCppSource
	}
	return 0, ""
}
