package llm

import (
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	anthropicprovider "github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	bedrockprovider "github.com/lycaon/lycaon/internal/llm/providers/bedrock"
	ollamaprovider "github.com/lycaon/lycaon/internal/llm/providers/ollama"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	vertexexpressprovider "github.com/lycaon/lycaon/internal/llm/providers/vertexexpress"
)

// hydrateProviderModels merges live-discovered model metadata into a provider
// instance so request controls (thinking, context) match List() output.
func hydrateProviderModels(p modelcall.Provider, effective []modelinfo.Entry) modelcall.Provider {
	if lp, ok := p.(*loggingProvider); ok {
		next := *lp
		next.inner = hydrateProviderModels(lp.inner, effective)
		return &next
	}
	if op, ok := p.(*openaicompat.Provider); ok {
		return op.WithEffectiveModels(effective)
	}
	if ap, ok := p.(*anthropicprovider.Provider); ok {
		return ap.WithEffectiveModels(effective)
	}
	if op, ok := p.(*ollamaprovider.Provider); ok {
		return op.WithEffectiveModels(effective)
	}
	if bp, ok := p.(*bedrockprovider.Provider); ok {
		return bp.WithEffectiveModels(effective)
	}
	if vp, ok := p.(*vertexexpressprovider.Provider); ok {
		return vp.WithEffectiveModels(effective)
	}
	if cf, ok := p.(*openaicompat.CloudflareProvider); ok {
		return cf.WithEffectiveModels(effective)
	}
	return p
}
