package openaicompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	openai "github.com/sashabaranov/go-openai"
)

func (p *Provider) postChatCompletion(ctx context.Context, model string, body []byte, reqHeaders map[string]string) (*chatCompletionResponseWire, error) {
	resp, err := p.postChatWithHTTPRetry(ctx, model, body, p.completeClient, reqHeaders)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := providerhttp.ReadCompletionResponse("openai", resp)
	if err != nil {
		return nil, failure.InterruptedResponse(ctx, p.id, model, err)
	}
	var wire chatCompletionResponseWire
	if err := json.Unmarshal(respBody, &wire); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}
	return &wire, nil
}

// postChatWithHTTPRetry POSTs until a 2xx response or the http_retry budget is spent.
func (p *Provider) postChatWithHTTPRetry(ctx context.Context, model string, body []byte, client *http.Client, reqHeaders map[string]string) (*http.Response, error) {
	return providerretry.RunProviderAttempts(ctx, providerretry.ProviderAttempt{
		ProviderID:   p.id,
		Model:        model,
		RefusalCodes: providerprofile.ModelRefusalCodesFor(p.Profile()),
		Policy:       p.httpRetry,
		Send: func(attemptCtx context.Context) (*http.Response, error) {
			return p.postChat(attemptCtx, model, body, client, reqHeaders)
		},
		Describe: func(status int, respBody []byte) error {
			return providerretry.FormatOpenAIProviderError(&openai.RequestError{
				HTTPStatusCode: status,
				Body:           respBody,
			})
		},
	})
}

func (p *Provider) postChat(ctx context.Context, model string, body []byte, client *http.Client, reqHeaders map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.chatCompletionURL(model), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	credential := p.apiKey
	if p.tokenSource != nil {
		tok, tokErr := p.tokenSource(ctx)
		if tokErr != nil {
			return nil, tokErr
		}
		credential = tok
	}
	if strings.TrimSpace(credential) != "" {
		req.Header.Set(p.http.authHeader, p.http.authScheme+credential)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range p.extraHeaders {
		req.Header.Set(k, v)
	}
	for k, v := range reqHeaders {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			continue
		}
		req.Header.Set(k, v)
	}
	if client == nil {
		client = providerhttp.RequestClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// promptCacheRequestHeaders returns projected prompt-cache headers.
func (p *Provider) promptCacheRequestHeaders(req modelcall.CompletionRequest) map[string]string {
	req.Model = p.ResolveModel(req)
	entry, _ := p.ModelEntry(req.Model)
	proj := providerwire.ProjectPromptCache(req, p.profile.PromptCache, entry.EffectiveCapabilities())
	if proj.SessionAffinityHeader == "" || proj.SessionAffinityValue == "" {
		return nil
	}
	return map[string]string{proj.SessionAffinityHeader: proj.SessionAffinityValue}
}

// chatCompletionURL builds the configured chat-completion endpoint.
func (p *Provider) chatCompletionURL(_ string) string {
	base := strings.TrimRight(p.baseURL, "/")
	return base + "/chat/completions"
}

func (p *Provider) streamChatCompletionFromResponse(ctx context.Context, req modelcall.CompletionRequest, resp *http.Response) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()

		var sawOutput bool
		var sawTerminal bool
		var usage modelcall.TokenUsage
		var finishReason string
		toolAcc := make(map[int]*providerwire.StreamTool)
		reasoningAcc := newReasoningDetailAccumulator()
		var lastToolProgress time.Time

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				sawTerminal = true
				break
			}
			var chunk chatCompletionStreamChunkWire
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: fmt.Errorf("openai-compatible: malformed SSE payload: %w", err), Done: true})
				return
			}
			if chunk.Usage != nil {
				usage = NormalizeUsage(chunk.Usage)
			}
			if len(chunk.Choices) == 0 {
				continue
			}
			choice := chunk.Choices[0]
			if choice.FinishReason != "" {
				finishReason = string(choice.FinishReason)
				sawTerminal = true
			}
			delta := choice.Delta
			if delta.Content != "" {
				sawOutput = true
				if !modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Content: delta.Content}) {
					return
				}
			}
			if reasoning := reasoningWireText(delta.Reasoning, delta.ReasoningContent); reasoning != "" {
				if !modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Reasoning: reasoning}) {
					return
				}
			}
			if len(delta.ReasoningDetails) > 0 {
				before := reasoningAcc.reasoningText()
				reasoningAcc.merge(delta.ReasoningDetails)
				// Block-only reasoning advances stream liveness.
				if text := strings.TrimPrefix(reasoningAcc.reasoningText(), before); text != "" &&
					reasoningWireText(delta.Reasoning, delta.ReasoningContent) == "" {
					if !modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Reasoning: text}) {
						return
					}
				}
			}
			if len(delta.ToolCalls) > 0 {
				if err := mergeStreamToolDeltas(toolAcc, delta.ToolCalls); err != nil {
					modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: fmt.Errorf("openai-compatible: malformed tool stream: %w", err), Done: true})
					return
				}
				sawOutput = true
				if partial := providerwire.CollectToolCalls(toolAcc, false, false); len(partial) > 0 {
					now := time.Now()
					if lastToolProgress.IsZero() || now.Sub(lastToolProgress) >= 500*time.Millisecond {
						lastToolProgress = now
						if !modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{ToolCalls: partial, Progress: true}) {
							return
						}
					}
				}
			}
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
			// Preserve partial-stream failures as terminal errors.
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: failure.InterruptedResponse(ctx, p.id, p.ResolveModel(req), err), Done: true})
			return
		}
		// Reasoning details are complete only at stream end.
		var reasoningDetails []json.RawMessage
		if !reasoningAcc.empty() {
			reasoningDetails = reasoningAcc.details()
		}
		if !sawTerminal {
			if !sawOutput && len(toolAcc) == 0 {
				modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: &failure.ProviderEmptyCompletionError{
					ProviderID: p.id,
					Model:      p.ResolveModel(req),
					Retryable:  true,
					Reason:     "stream ended before a terminal event",
				}, Usage: usage, Done: true})
				return
			}
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: failure.InterruptedResponse(ctx, p.id, p.ResolveModel(req), io.ErrUnexpectedEOF), Done: true})
			return
		}
		if len(toolAcc) > 0 {
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{
				ToolCalls:        providerwire.CollectToolCalls(toolAcc, finishReason == string(openai.FinishReasonLength), true),
				ReasoningDetails: reasoningDetails,
				Usage:            usage,
				Done:             true,
			})
			return
		}
		if !sawOutput {
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: &failure.ProviderEmptyCompletionError{
				ProviderID: p.id,
				Model:      p.ResolveModel(req),
				Retryable:  EmptyCompletionRetryable(finishReason),
				Reason:     finishReason,
				Terminal:   finishReason != "",
			}, Usage: usage, Done: true})
			return
		}
		modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{ReasoningDetails: reasoningDetails, Usage: usage, Done: true, Err: modelcall.OutputTruncation(req, p.id, p.ResolveModel(req), finishReason, usage, false)})
	}()
	return ch, nil
}
