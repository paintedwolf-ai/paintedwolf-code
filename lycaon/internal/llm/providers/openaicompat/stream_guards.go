package openaicompat

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

func (p *Provider) streamAttempt(ctx context.Context, req modelcall.CompletionRequest, opts controlOpts) (<-chan modelcall.StreamChunk, error) {
	body, err := encodeChatCompletionRequest(req, p, true, opts)
	if err != nil {
		return nil, err
	}

	return p.openStreamBody(ctx, req, body, opts)
}

func (p *Provider) openStreamBody(ctx context.Context, req modelcall.CompletionRequest, body []byte, opts controlOpts) (<-chan modelcall.StreamChunk, error) {
	req.ControlCapture.Record(body)
	resp, err := p.postChatWithHTTPRetry(ctx, p.ResolveModel(req), body, p.streamClient, p.promptCacheRequestHeaders(req)) //nolint:bodyclose // Stream consumers close the body.
	if err != nil {
		if retryCh, retryErr, ok := p.retryStreamAfterFormattedError(ctx, req, opts, err); ok {
			return retryCh, retryErr
		}
		return nil, err
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		return p.streamFromJSONResponse(ctx, req, resp)
	}
	return p.streamChatCompletionFromResponse(ctx, req, resp)
}

func (p *Provider) retryStreamAfterFormattedError(ctx context.Context, req modelcall.CompletionRequest, opts controlOpts, err error) (<-chan modelcall.StreamChunk, error, bool) {
	if req.ThinkingOverride != nil {
		return nil, err, false
	}
	if _, rateLimited := failure.AsProviderRateLimited(err); rateLimited {
		return nil, nil, false
	}
	if _, overloaded := failure.AsProviderOverloaded(err); overloaded {
		return nil, nil, false
	}
	model := p.ResolveModel(req)
	current := p.reasoningFallbackFor(model, opts)
	if current == reasoningFallbackNone && !p.requestHasReasoningControl(req, model, opts) {
		return nil, nil, false
	}
	next, ok := nextReasoningFallback(err, current, p.profile.ReasoningEffortOff != "")
	if !ok {
		return nil, nil, false
	}
	retryOpts := controlOpts{
		strictRetry: opts.strictRetry,
		reasoning:   next,
	}
	ch, retryErr := p.streamAttempt(ctx, req, retryOpts)
	if retryErr != nil {
		return nil, err, true
	}
	markReasoningFallback(p.id, req.Model, next)
	return ch, nil, true
}
