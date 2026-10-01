package llm

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/observability"
)

type loggingClient struct {
	inner      modelcall.LLMClient
	providerID string
}

// WrapLLMClientIfDebug wraps inner when LYCAON_LLM_DEBUG is enabled.
func WrapLLMClientIfDebug(inner modelcall.LLMClient, providerID string) modelcall.LLMClient {
	if inner == nil || !observability.LLMDebugEnabled() {
		return inner
	}
	if _, ok := inner.(*loggingClient); ok {
		return inner
	}
	if providerID == "" {
		providerID = "unknown"
	}
	return &loggingClient{inner: inner, providerID: providerID}
}

func (c *loggingClient) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return captureLLMComplete(c.providerID, providerprofile.Default(), req, func() (*modelcall.Completion, error) {
		return c.inner.Complete(ctx, req)
	})
}

func (c *loggingClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	return captureLLMStream(ctx, c.providerID, providerprofile.Default(), req, func() (<-chan modelcall.StreamChunk, error) {
		return c.inner.Stream(ctx, req)
	})
}

var _ modelcall.LLMClient = (*loggingClient)(nil)
