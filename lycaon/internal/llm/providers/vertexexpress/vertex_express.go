// Package vertexexpress implements the native Vertex express content protocol and stream decoder.
package vertexexpress

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

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// vertexExpressDefaultMaxTokens is the fallback completion cap.
const vertexExpressDefaultMaxTokens = 8192

// vertexExpressPublisherPath scopes requests to first-party publisher models.
const vertexExpressPublisherPath = "/publishers/google/models/"

// Provider calls the native express-mode content API.
// It uses API-key authentication and a global publisher-model endpoint.
type Provider struct {
	id        string
	baseURL   string
	apiKey    string
	profile   providerprofile.Profile
	client    *http.Client
	httpRetry providerretry.ProviderHTTPRetry
	modelcall.Catalog
}

// New constructs the native Vertex express AI provider.
func New(id, baseURL, apiKey string, models []modelinfo.Entry) *Provider {
	return &Provider{
		id:      id,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		Catalog: modelcall.NewCatalog("vertex-express", models),
		profile: providerprofile.VertexExpress(),
		client:  providerhttp.RequestClient(),
	}
}

// WithEffectiveModels replaces the model catalog with the merged model list.
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

// Prepare projects a completion onto the native content wire.
func (p *Provider) Prepare(req modelcall.CompletionRequest) Request {
	model := p.ResolveModel(req)
	entry, _ := p.ModelEntry(model)
	vision := modelinfo.Supported(entry.EffectiveCapabilities().Vision)
	req.Messages = providerwire.PrepareMessagesForVision(req.Messages, vision, req.Debug.SessionID)
	system, contents := ProjectMessages(req.Messages, vision, req.Debug.SessionID)

	cfg := p.resolveRequestControls(req, model)
	out := Request{Contents: contents, SystemInstruction: system, Tools: ProjectTools(req.Tools), GenerationConfig: &cfg}
	if len(req.Tools) > 0 && !req.ToolsCallable() {
		out.ToolConfig = &vertexExpressToolConfig{FunctionCallingConfig: vertexExpressFunctionCallingConfig{Mode: "NONE"}}
	}
	return out
}

// modelURL builds a global endpoint with optional SSE framing.
func (p *Provider) modelURL(model, method string, stream bool) string {
	url := p.baseURL + vertexExpressPublisherPath + model + ":" + method
	if stream {
		url += "?alt=sse"
	}
	return url
}

func (p *Provider) newHTTPRequest(ctx context.Context, url string, body []byte) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// Keep the key out of request URLs and URL logs.
	httpReq.Header.Set("x-goog-api-key", p.apiKey)
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "application/json")
	return httpReq, nil
}

func (p *Provider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	body, err := surveyjson.Marshal(p.Prepare(req))
	if err != nil {
		return nil, err
	}
	model := p.ResolveModel(req)
	url := p.modelURL(model, "generateContent", false)
	req.ControlCapture.Record(body)
	resp, err := p.doGenerateContentWithHTTPRetry(ctx, model, url, body)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := providerhttp.ReadCompletionResponse("vertex express", resp)
	if err != nil {
		return nil, err
	}
	var wire vertexExpressResponse
	if err := json.Unmarshal(respBody, &wire); err != nil {
		return nil, fmt.Errorf("vertex express: decode response: %w", err)
	}
	if len(wire.Candidates) == 0 {
		reason := ""
		retryable := true
		if wire.PromptFeedback != nil {
			reason = strings.TrimSpace(wire.PromptFeedback.BlockReason)
			retryable = reason == ""
		}
		return nil, &failure.ProviderEmptyCompletionError{
			ProviderID: p.id,
			Model:      model,
			Retryable:  retryable,
			Reason:     reason,
		}
	}
	out := mapVertexExpressResponse(&wire)
	if out == nil {
		return nil, &failure.ProviderEmptyCompletionError{ProviderID: p.id, Model: model, Retryable: true}
	}
	// Reasoning alone does not make a transcript turn nonempty.
	hasOutput := out.Content != "" || len(out.ToolCalls) > 0
	if err := vertexExpressEmptyOutputErr(p.id, model, wire.Candidates[0].FinishReason, hasOutput); err != nil {
		return out, err
	}
	return out, modelcall.OutputTruncation(req, p.id, model, wire.Candidates[0].FinishReason, out.Usage, len(out.ToolCalls) > 0)
}

func (p *Provider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	body, err := surveyjson.Marshal(p.Prepare(req))
	if err != nil {
		return nil, err
	}
	model := p.ResolveModel(req)
	url := p.modelURL(model, "streamGenerateContent", true)
	req.ControlCapture.Record(body)
	resp, err := p.doGenerateContentWithHTTPRetry(ctx, model, url, body) //nolint:bodyclose // The reader goroutine closes the body.
	if err != nil {
		return nil, err
	}
	return p.streamFromResponse(ctx, req, resp), nil
}

func (p *Provider) doGenerateContentWithHTTPRetry(ctx context.Context, model, url string, body []byte) (*http.Response, error) {
	return providerretry.RunProviderAttempts(ctx, providerretry.ProviderAttempt{
		ProviderID:   p.id,
		Model:        model,
		RefusalCodes: providerprofile.ModelRefusalCodesFor(p.Profile()),
		Policy:       p.httpRetry,
		Send: func(attemptCtx context.Context) (*http.Response, error) {
			httpReq, err := p.newHTTPRequest(attemptCtx, url, body)
			if err != nil {
				return nil, err
			}
			return p.client.Do(httpReq)
		},
		Describe: vertexExpressHTTPError,
	})
}

// streamFromResponse folds full SSE frames into incremental host chunks.
// Usage totals replace earlier values.
func (p *Provider) streamFromResponse(ctx context.Context, req modelcall.CompletionRequest, resp *http.Response) <-chan modelcall.StreamChunk {
	model := p.ResolveModel(req)
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()

		var usage modelcall.TokenUsage
		var toolCalls []api.ToolCall
		var finishReason string
		var promptBlockReason string
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
			var frame vertexExpressResponse
			if err := json.Unmarshal([]byte(payload), &frame); err != nil {
				modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: fmt.Errorf("vertex express: malformed SSE payload: %w", err), Done: true})
				return
			}
			if frame.UsageMetadata != nil {
				usage = tokenUsageFromVertexExpress(frame.UsageMetadata)
			}
			if frame.PromptFeedback != nil && strings.TrimSpace(frame.PromptFeedback.BlockReason) != "" {
				promptBlockReason = strings.TrimSpace(frame.PromptFeedback.BlockReason)
			}
			if len(frame.Candidates) == 0 {
				continue
			}
			if r := strings.TrimSpace(frame.Candidates[0].FinishReason); r != "" {
				finishReason = r
				sawTerminal = true
			}
			for _, part := range frame.Candidates[0].Content.Parts {
				switch {
				case part.FunctionCall != nil:
					sawOutput = true
					toolCalls = append(toolCalls, api.ToolCall{
						ID:   providerwire.NewToolCallID(),
						Name: part.FunctionCall.Name,
						Args: part.FunctionCall.Args,
					})
					if !modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{ToolCalls: toolCalls, Progress: true}) {
						return
					}
				case part.Thought:
					// Reasoning alone does not make a transcript turn nonempty.
					if part.Text != "" {
						if !modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Reasoning: part.Text}) {
							return
						}
					}
				default:
					if part.Text != "" {
						sawOutput = true
						if !modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Content: part.Text}) {
							return
						}
					}
				}
			}
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: err, Done: true})
			return
		}
		if !sawTerminal {
			if !sawOutput {
				retryable := promptBlockReason == ""
				modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: &failure.ProviderEmptyCompletionError{
					ProviderID: p.id,
					Model:      model,
					Retryable:  retryable,
					Reason:     promptBlockReason,
				}, Usage: usage, Done: true})
				return
			}
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: fmt.Errorf("vertex express: stream ended before a terminal finishReason"), Usage: usage, Done: true})
			return
		}
		// Preserve the finish reason for an empty filtered stream.
		if err := vertexExpressEmptyOutputErr(p.id, model, finishReason, sawOutput); err != nil {
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: err, Usage: usage, Done: true})
			return
		}
		modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{ToolCalls: toolCalls, Usage: usage, Done: true, Err: modelcall.OutputTruncation(req, p.id, model, finishReason, usage, len(toolCalls) > 0)})
	}()
	return ch
}

func vertexExpressHTTPError(status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	// Error bodies may be wrapped in a single-element array.
	var single struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &single) == nil && single.Error.Message != "" {
		msg = single.Error.Message
	} else {
		var batch []struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &batch) == nil && len(batch) > 0 && batch[0].Error.Message != "" {
			msg = batch[0].Error.Message
		}
	}
	return fmt.Errorf("vertex express: HTTP %d: %s", status, msg)
}

var _ modelcall.Provider = (*Provider)(nil)
