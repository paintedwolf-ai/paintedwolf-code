package llm

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/observability"
)

type loggingProvider struct {
	inner modelcall.Provider
}

func wrapProviderIfDebug(p modelcall.Provider) modelcall.Provider {
	if p == nil || !observability.LLMDebugEnabled() {
		return p
	}
	if _, ok := p.(*loggingProvider); ok {
		return p
	}
	return &loggingProvider{inner: p}
}

func (p *loggingProvider) ID() string { return p.inner.ID() }

func (p *loggingProvider) Models() []modelcall.ModelInfo { return p.inner.Models() }

func (p *loggingProvider) Profile() providerprofile.Profile { return p.inner.Profile() }

func (p *loggingProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	req.ControlCapture = &modelcall.RequestControlCapture{}
	return captureLLMComplete(p.inner.ID(), p.inner.Profile(), req, func() (*modelcall.Completion, error) {
		return p.inner.Complete(ctx, req)
	})
}

func (p *loggingProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	req.ControlCapture = &modelcall.RequestControlCapture{}
	return captureLLMStream(ctx, p.inner.ID(), p.inner.Profile(), req, func() (<-chan modelcall.StreamChunk, error) {
		return p.inner.Stream(ctx, req)
	})
}

var _ modelcall.Provider = (*loggingProvider)(nil)
