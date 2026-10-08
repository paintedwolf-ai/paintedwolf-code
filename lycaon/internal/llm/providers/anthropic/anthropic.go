// Package anthropic implements Anthropic message preparation, signed reasoning replay, and SSE decoding.
package anthropic

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

	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

type Provider struct {
	id        string
	baseURL   string
	apiKey    string
	profile   providerprofile.Profile
	client    *http.Client
	httpRetry providerretry.ProviderHTTPRetry
	modelcall.Catalog
}

func New(id, baseURL, apiKey string, models []modelinfo.Entry) *Provider {
	return &Provider{
		id:      id,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		Catalog: modelcall.NewCatalog("anthropic", models),
		profile: providerprofile.Anthropic(),
		client:  providerhttp.RequestClient(),
	}
}

// WithEffectiveModels replaces the model catalog with a live-discovered list.
func (p *Provider) WithEffectiveModels(models []modelinfo.Entry) *Provider {
	next := *p
	next.Catalog = p.WithModels(models)
	return &next
}

// WithHTTPRetry sets the per-provider chat HTTP retry policy.
func (p *Provider) WithHTTPRetry(policy providerretry.ProviderHTTPRetry) *Provider {
	p.httpRetry = policy.Clone()
	return p
}

// WithPromptCache sets the provider's prompt-cache policy.
func (p *Provider) WithPromptCache(policy providerprofile.PromptCachePolicy) *Provider {
	p.profile.PromptCache = policy
	return p
}

func (p *Provider) ID() string                       { return p.id }
func (p *Provider) Profile() providerprofile.Profile { return p.profile }

// Prepare projects a completion onto the block-structured wire.
func (p *Provider) Prepare(req modelcall.CompletionRequest, stream bool) Request {
	model := p.ResolveModel(req)
	entry, _ := p.ModelEntry(model)
	vision := modelinfo.Supported(entry.EffectiveCapabilities().Vision)
	req.Messages = providerwire.PrepareMessagesForVision(req.Messages, vision, req.Debug.SessionID)
	proj := providerwire.ProjectPromptCache(req, p.profile.PromptCache, entry.EffectiveCapabilities())
	breakpoints := providerwire.TrailingPromptCacheBreakpoints(proj.Breakpoints, providerwire.AnthropicExplicitBreakpointLimit)
	system, messages := ProjectMessages(req.Messages, breakpoints, vision, req.Debug.SessionID, p.id, model)

	out := p.resolveRequestControls(req, model)
	out.System = system
	out.Messages = messages
	out.Stream = stream
	if proj.RequestMarker {
		out.CacheControl = &anthropicCacheControl{Type: "ephemeral", TTL: providerwire.LifetimeToken(proj.RequestLifetime)}
	}
	if len(req.Tools) > 0 {
		out.Tools = ProjectTools(req.Tools)
		if !req.ToolsCallable() {
			out.ToolChoice = &anthropicToolChoice{Type: "none"}
		}
	}
	return out
}

func (p *Provider) newHTTPRequest(ctx context.Context, body []byte) (*http.Request, error) {
	url := p.baseURL + "/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", discovery.AnthropicVersion)
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "application/json")
	return httpReq, nil
}

func (p *Provider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	body, err := surveyjson.Marshal(p.Prepare(req, false))
	if err != nil {
		return nil, err
	}
	req.ControlCapture.Record(body)
	resp, err := p.doMessagesWithHTTPRetry(ctx, p.ResolveModel(req), body)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := providerhttp.ReadCompletionResponse("anthropic", resp)
	if err != nil {
		return nil, err
	}
	var wire Response
	if err := json.Unmarshal(respBody, &wire); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}
	out, err := mapAnthropicResponse(&wire)
	if err != nil {
		return nil, err
	}
	if !modelcall.CompletionHasPayload(out) {
		return out, &failure.ProviderEmptyCompletionError{
			ProviderID: p.id,
			Model:      p.ResolveModel(req),
			Retryable:  EmptyCompletionRetryable(wire.StopReason),
			Reason:     wire.StopReason,
			Terminal:   wire.StopReason != "",
		}
	}
	return out, modelcall.OutputTruncation(req, p.id, p.ResolveModel(req), wire.StopReason, out.Usage, len(out.ToolCalls) > 0)
}

func (p *Provider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	body, err := surveyjson.Marshal(p.Prepare(req, true))
	if err != nil {
		return nil, err
	}
	req.ControlCapture.Record(body)
	resp, err := p.doMessagesWithHTTPRetry(ctx, p.ResolveModel(req), body) //nolint:bodyclose // The reader goroutine closes the body.
	if err != nil {
		return nil, err
	}
	return p.streamFromResponse(ctx, req, resp), nil
}

func (p *Provider) doMessagesWithHTTPRetry(ctx context.Context, model string, body []byte) (*http.Response, error) {
	return providerretry.RunProviderAttempts(ctx, providerretry.ProviderAttempt{
		ProviderID:   p.id,
		Model:        model,
		RefusalCodes: providerprofile.ModelRefusalCodesFor(p.Profile()),
		Policy:       p.httpRetry,
		Send: func(attemptCtx context.Context) (*http.Response, error) {
			httpReq, err := p.newHTTPRequest(attemptCtx, body)
			if err != nil {
				return nil, err
			}
			return p.client.Do(httpReq)
		},
		Describe: anthropicHTTPError,
	})
}

// Data frames carry their event type in the payload.
type anthropicStreamEvent struct {
	Type         string                `json:"type"`
	Index        int                   `json:"index"`
	Message      *anthropicStreamStart `json:"message"`
	ContentBlock *ContentBlock         `json:"content_block"`
	Delta        *anthropicStreamDelta `json:"delta"`
	Usage        *Usage                `json:"usage"`
}

type anthropicStreamStart struct {
	Usage *Usage `json:"usage"`
}

type anthropicStreamDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	PartialJSON string `json:"partial_json"`
	Thinking    string `json:"thinking"`
	Signature   string `json:"signature"`
	StopReason  string `json:"stop_reason"`
}

func (p *Provider) streamFromResponse(ctx context.Context, req modelcall.CompletionRequest, resp *http.Response) <-chan modelcall.StreamChunk {
	model := p.ResolveModel(req)
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()

		var usage modelcall.TokenUsage
		var inputReported bool
		var stopReason string
		toolAcc := make(map[int]*providerwire.StreamTool)
		reasoningBlocks := make(map[int]*ContentBlock)
		var lastProgress time.Time
		sawOutput := false
		sawTerminal := false

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" {
				continue
			}
			var ev anthropicStreamEvent
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: fmt.Errorf("anthropic: malformed SSE payload: %w", err), Done: true})
				return
			}
			switch ev.Type {
			case "message_start":
				if ev.Message != nil {
					// Input and output usage arrive in separate events.
					start := NormalizeUsage(ev.Message.Usage)
					usage = start
					usage.Incomplete = true
					inputReported = ev.Message.Usage != nil && ev.Message.Usage.InputTokens != nil
					if usage.Reported() && !modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Usage: usage}) {
						return
					}
				}
			case "content_block_start":
				if ev.ContentBlock != nil && (ev.ContentBlock.Type == "thinking" || ev.ContentBlock.Type == "redacted_thinking") {
					copyBlock := *ev.ContentBlock
					reasoningBlocks[ev.Index] = &copyBlock
				}
				if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
					sawOutput = true
					slot := &providerwire.StreamTool{ID: providerwire.NewToolCallID(), WireID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name}
					toolAcc[ev.Index] = slot
					if ev.ContentBlock.Name != "" {
						lastProgress = time.Now()
						if partial := providerwire.CollectToolCalls(toolAcc, false, false); len(partial) > 0 {
							if !modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{ToolCalls: partial, Progress: true}) {
								return
							}
						}
					}
				}
			case "content_block_delta":
				if ev.Delta == nil {
					continue
				}
				switch ev.Delta.Type {
				case "text_delta":
					if ev.Delta.Text != "" {
						sawOutput = true
						if !modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Content: ev.Delta.Text}) {
							return
						}
					}
				case "thinking_delta":
					if block := reasoningBlocks[ev.Index]; block != nil {
						block.Thinking += ev.Delta.Thinking
					}
					if ev.Delta.Thinking != "" {
						if !modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Reasoning: ev.Delta.Thinking}) {
							return
						}
					}
				case "signature_delta":
					if block := reasoningBlocks[ev.Index]; block != nil {
						block.Signature += ev.Delta.Signature
					}
				case "input_json_delta":
					if slot := toolAcc[ev.Index]; slot != nil && ev.Delta.PartialJSON != "" {
						slot.Args.WriteString(ev.Delta.PartialJSON)
						if now := time.Now(); lastProgress.IsZero() || now.Sub(lastProgress) >= 500*time.Millisecond {
							lastProgress = now
							if partial := providerwire.CollectToolCalls(toolAcc, false, false); len(partial) > 0 {
								if !modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{ToolCalls: partial, Progress: true}) {
									return
								}
							}
						}
					}
				}
			case "message_delta":
				if ev.Delta != nil && ev.Delta.StopReason != "" {
					stopReason = ev.Delta.StopReason
				}
				if ev.Usage != nil && ev.Usage.OutputTokens != nil {
					usage.Present = true
					usage.Incomplete = !inputReported
					usage.CompletionTokens = *ev.Usage.OutputTokens
					if !modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Usage: usage}) {
						return
					}
				}
			case "error":
				modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: fmt.Errorf("anthropic: stream error: %s", payload), Done: true})
				return
			case "message_stop":
				sawTerminal = true
			}
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: err, Done: true})
			return
		}
		if !sawTerminal {
			if !sawOutput && len(toolAcc) == 0 {
				modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: &failure.ProviderEmptyCompletionError{
					ProviderID: p.id,
					Model:      model,
					Retryable:  true,
					Reason:     "stream ended before message_stop",
				}, Usage: usage, Done: true})
				return
			}
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: fmt.Errorf("anthropic: stream ended before message_stop"), Done: true})
			return
		}
		if !sawOutput {
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: &failure.ProviderEmptyCompletionError{
				ProviderID: p.id,
				Model:      model,
				Retryable:  EmptyCompletionRetryable(stopReason),
				Reason:     stopReason,
				Terminal:   stopReason != "",
			}, Usage: usage, Done: true})
			return
		}
		if len(toolAcc) > 0 {
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{
				ToolCalls:        providerwire.CollectToolCalls(toolAcc, stopReason == "max_tokens", true),
				ReasoningDetails: anthropicReasoningDetails(reasoningBlocks),
				Usage:            usage,
				Done:             true,
			})
			return
		}
		modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Usage: usage, ReasoningDetails: anthropicReasoningDetails(reasoningBlocks), Done: true, Err: modelcall.OutputTruncation(req, p.id, model, stopReason, usage, false)})
	}()
	return ch
}

func EmptyCompletionRetryable(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "", "end_turn", "stop_sequence", "tool_use":
		return true
	default:
		return false
	}
}

func anthropicHTTPError(status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	var parsed struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Error.Message != "" {
		msg = parsed.Error.Message
	}
	return fmt.Errorf("anthropic: HTTP %d: %s", status, msg)
}

var _ modelcall.Provider = (*Provider)(nil)
