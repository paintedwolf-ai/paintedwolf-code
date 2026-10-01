// Package ollama implements the native Ollama chat protocol, context sizing, and stream decoding.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

const ollamaProbeTimeout = 3 * time.Second

const (
	ollamaInitialPromptScaleMillis = 1500
	ollamaMaxPromptScaleMillis     = 8000
)

type Provider struct {
	id             string
	nativeBase     string
	apiKey         string
	streamClient   *http.Client
	completeClient *http.Client
	profile        providerprofile.Profile
	httpRetry      providerretry.ProviderHTTPRetry
	modelcall.Catalog

	capMu    sync.Mutex
	capCache map[string]int

	promptMu          sync.Mutex
	promptScaleMillis map[string]int
}

func New(id, baseURL, apiKey string, models []modelinfo.Entry) *Provider {
	profile := providerprofile.Ollama()
	return &Provider{
		id:                id,
		nativeBase:        discovery.OllamaNativeBase(baseURL),
		apiKey:            apiKey,
		streamClient:      providerhttp.NewStreamingClient(profile.StreamResponseHeaderTimeout),
		completeClient:    providerhttp.NewStreamingClient(profile.CompleteResponseHeaderTimeout),
		Catalog:           modelcall.NewCatalog("ollama", models),
		profile:           profile,
		capCache:          make(map[string]int),
		promptScaleMillis: make(map[string]int),
	}
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

func (p *Provider) ID() string { return p.id }

func (p *Provider) Profile() providerprofile.Profile { return p.profile }

// WithEffectiveModels returns an isolated runtime with discovery evidence.
func (p *Provider) WithEffectiveModels(models []modelinfo.Entry) *Provider {
	next := New(p.id, p.nativeBase, p.apiKey, models)
	next.streamClient = p.streamClient
	next.completeClient = p.completeClient
	next.profile = p.profile
	next.httpRetry = p.httpRetry.Clone()
	p.capMu.Lock()
	for model, contextLength := range p.capCache {
		next.capCache[model] = contextLength
	}
	p.capMu.Unlock()
	p.promptMu.Lock()
	for model, scale := range p.promptScaleMillis {
		next.promptScaleMillis[model] = scale
	}
	p.promptMu.Unlock()
	return next
}

// resolveControls applies sampling and completion limits.
func (p *Provider) resolveControls(req modelcall.CompletionRequest, model string) (temperature *float64, numPredict int) {
	if entry, ok := p.ModelEntry(model); ok {
		if entry.Temperature != nil {
			t := *entry.Temperature
			temperature = &t
		}
	}
	entry, _ := p.ModelEntry(model)
	think := p.resolveThink(req, model)
	reducedReq := req
	reducedReq.StrictBudget = true
	reducedReq.AttemptBudget = nil
	reduced := p.resolveThink(reducedReq, model)
	level := modelcall.ResolveThinkLevel(req, entry.ReasoningEffort, req.StrictOutputBudget())
	numPredict = modelcall.CompletionTokenLimit(req, entry.MaxTokens, 0, level, think != nil && think != false)
	req.RecordBudget(numPredict, think, reduced)
	mt := modelcall.ResolveModelThinking(p.profile, entry, model)
	if req.AttemptBudget != nil && mt.AlwaysOn && mt.Style == modelinfo.ThinkStyleBooleanThink {
		req.AttemptBudget.CanReduceReasoning = false
	}
	return temperature, numPredict
}

// resolveThink maps the shared thinking policy onto the native control.
// Nil omits the control; false disables it; strings preserve named levels.
func (p *Provider) resolveThink(req modelcall.CompletionRequest, model string) any {
	if override := req.ThinkingOverride; override != nil {
		if override.Enabled != nil {
			return *override.Enabled
		}
		return override.Effort
	}
	entry, ok := p.ModelEntry(model)
	if !ok {
		entry = modelinfo.Entry{}
	}
	mt := modelcall.ResolveModelThinking(p.profile, entry, model)
	if mt.Style == modelinfo.ThinkStyleNone {
		return nil
	}
	strict := req.StrictOutputBudget()
	level := modelcall.ResolveThinkLevel(req, entry.ReasoningEffort, strict)
	think := level.OllamaThink()
	if mt.Style == modelinfo.ThinkStyleEffortLevels {
		if effort, send := mt.EffortString(level); send {
			think = effort
		}
	}
	if mt.Style == modelinfo.ThinkStyleBooleanThink && level != modelcall.ThinkUnset {
		think = level != modelcall.ThinkOff
	}
	if mt.AlwaysOn {
		if b, isBool := think.(bool); isBool && !b {
			return nil
		}
	}
	return think
}

// estimatePromptTokens includes messages, tool calls, and tool schemas.
func estimatePromptTokens(req modelcall.CompletionRequest) int {
	total := 0
	for _, m := range req.Messages {
		if api.IsAgentNoteMessage(m) {
			continue
		}
		content := m.Content
		if m.Role == api.MessageRoleTool && content == "" && m.ToolResult != nil {
			content = m.ToolResult.Content
		}
		total += tokenest.EstimateDefault(content)
		for _, tc := range m.ToolCalls {
			total += tokenest.EstimateDefault(tc.Name)
			if len(tc.Args) > 0 {
				if raw, err := json.Marshal(tc.Args); err == nil {
					total += tokenest.EstimateDefault(string(raw))
				}
			}
		}
	}
	for _, t := range req.Tools {
		total += tokenest.EstimateDefault(t.Name) + tokenest.EstimateDefault(t.Description)
		if t.ArgsSchema != nil {
			if raw, err := json.Marshal(t.ArgsSchema); err == nil {
				total += tokenest.EstimateDefault(string(raw))
			}
		}
	}
	return total
}

// Prepare validates context fit and assembles the native payload.
func (p *Provider) Prepare(ctx context.Context, req modelcall.CompletionRequest, stream bool) (Request, int, error) {
	model := p.ResolveModel(req)
	entry, _ := p.ModelEntry(model)
	vision := modelinfo.Supported(entry.EffectiveCapabilities().Vision)
	req.Messages = providerwire.PrepareMessagesForVision(req.Messages, vision, req.Debug.SessionID)
	temperature, numPredict := p.resolveControls(req, model)
	promptTokens := estimatePromptTokens(req)
	projectedPromptTokens := p.projectPromptTokens(model, promptTokens)
	maxContext := p.ContextLimit(ctx, model)
	// Keep automatic answer room inside the model's actual context window.
	// An explicit request cap remains the caller's contract and must fit.
	if req.MaxTokens == 0 && maxContext > 0 && numPredict > 0 {
		available := maxContext - projectedPromptTokens - ollamaContextHeadroom
		if available > 0 && numPredict > available {
			numPredict = available
		}
	}
	if req.AttemptBudget != nil {
		req.AttemptBudget.MaxTokens = numPredict
	}
	completionReserve := numPredict
	if completionReserve <= 0 {
		completionReserve = ollamaDefaultCompletionReserve
	}
	numCtx, fits := sizeNumCtx(projectedPromptTokens, completionReserve, maxContext)
	if !fits {
		return Request{}, promptTokens, &failure.ProviderContextTooSmallError{
			ProviderID:        p.id,
			Model:             model,
			PromptTokens:      projectedPromptTokens,
			CompletionReserve: completionReserve,
			MaxContext:        maxContext,
		}
	}
	resident := p.residentContext(ctx, model)
	numCtx = preferResidentContext(numCtx, contextNeed(projectedPromptTokens, completionReserve), resident, maxContext)
	slog.DebugContext(ctx, "ollama request context sized",
		"component", "ollama_provider",
		"provider", p.id,
		"model", model,
		"purpose", req.Debug.Purpose,
		"prompt_tokens_estimated", promptTokens,
		"prompt_tokens_projected", projectedPromptTokens,
		"completion_tokens_reserved", completionReserve,
		"num_ctx", numCtx,
		"resident_ctx", resident,
		"max_context", maxContext)
	body := Request{
		Model:     model,
		Messages:  ProjectMessages(req.Messages, vision, req.Debug.SessionID),
		Tools:     toolsToOllama(req.Tools),
		Stream:    stream,
		KeepAlive: providerwire.ProjectPromptCache(req, p.profile.PromptCache, modelinfo.ModelCapabilities{}).KeepAlive,
		Options: ollamaOptions{
			NumCtx:      numCtx,
			NumPredict:  numPredict,
			Temperature: temperature,
		},
		Format: modelcall.ResponseFormatToOllama(req.ResponseFormat),
	}
	body.Think = p.resolveThink(req, model)
	return body, promptTokens, nil
}

func (p *Provider) post(ctx context.Context, client *http.Client, payload Request) (*http.Response, error) {
	body, err := surveyjson.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.nativeBase+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	if client == nil {
		client = providerhttp.RequestClient()
	}
	return client.Do(req)
}

func (p *Provider) postWithHTTPRetry(ctx context.Context, client *http.Client, payload Request) (*http.Response, error) {
	return providerretry.RunProviderAttempts(ctx, providerretry.ProviderAttempt{
		ProviderID:   p.id,
		Model:        payload.Model,
		RefusalCodes: providerprofile.ModelRefusalCodesFor(p.Profile()),
		Policy:       p.httpRetry,
		Send: func(attemptCtx context.Context) (*http.Response, error) {
			return p.post(attemptCtx, client, payload)
		},
		Describe: func(status int, body []byte) error {
			return fmt.Errorf("ollama: chat HTTP %d: %s", status, strings.TrimSpace(string(body)))
		},
	})
}

func (p *Provider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	payload, promptEst, err := p.Prepare(ctx, req, false)
	if err != nil {
		return nil, err
	}
	if req.ControlCapture != nil {
		body, err := surveyjson.Marshal(payload)
		if err != nil {
			return nil, err
		}
		req.ControlCapture.Record(body)
	}
	resp, err := p.postWithHTTPRetry(ctx, p.completeClient, payload)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := providerhttp.ReadCompletionResponse("ollama", resp)
	if err != nil {
		return nil, err
	}
	var decoded ollamaChatResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}
	if decoded.Error != "" {
		return nil, fmt.Errorf("ollama: %s", decoded.Error)
	}
	p.warnIfTruncated(payload, promptEst, decoded.PromptEvalCount)
	p.observePromptTokens(payload.Model, promptEst, decoded.PromptEvalCount)
	providerwire.NoteOllamaPromptEvalDuration(providerwire.PromptCacheScope(p.id, payload.Model, req.Debug), decoded.PromptEvalDuration)
	out := &modelcall.Completion{
		Content:   decoded.Message.Content,
		Reasoning: decoded.Message.Thinking,
		ToolCalls: ollamaToolCallsToAPI(decoded.Message.ToolCalls),
		Usage:     modelcall.TokenUsage{PromptTokens: decoded.PromptEvalCount, CompletionTokens: decoded.EvalCount},
	}
	if decoded.DoneReason == "length" {
		return out, p.outputTruncatedError(payload, decoded.DoneReason, out.Usage)
	}
	if out.Content == "" && len(out.ToolCalls) == 0 {
		return out, &failure.ProviderEmptyCompletionError{ProviderID: p.id, Model: payload.Model, Retryable: true, Terminal: decoded.Done, Reason: decoded.DoneReason}
	}
	return out, nil
}

func (p *Provider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	payload, promptEst, err := p.Prepare(ctx, req, true)
	if err != nil {
		return nil, err
	}
	if req.ControlCapture != nil {
		body, err := surveyjson.Marshal(payload)
		if err != nil {
			return nil, err
		}
		req.ControlCapture.Record(body)
	}
	resp, err := p.postWithHTTPRetry(ctx, p.streamClient, payload) //nolint:bodyclose // the forwarding goroutine below closes the body after draining the decoder
	if err != nil {
		return nil, err
	}
	inner := decodeOllamaStream(ctx, resp.Body, p.id, payload.Model, payload.Options.NumCtx, func(promptTokens int, promptEvalDuration int64) {
		p.warnIfTruncated(payload, promptEst, promptTokens)
		p.observePromptTokens(payload.Model, promptEst, promptTokens)
		providerwire.NoteOllamaPromptEvalDuration(providerwire.PromptCacheScope(p.id, payload.Model, req.Debug), promptEvalDuration)
	})
	out := make(chan modelcall.StreamChunk)
	go func() {
		defer close(out)
		defer func() { _ = resp.Body.Close() }()
		for chunk := range inner {
			if !modelcall.SendChunk(ctx, out, chunk) {
				// Drain after consumer cancellation so the body can close.
				modelcall.DrainStream(inner)
				return
			}
		}
	}()
	return out, nil
}

func (p *Provider) outputTruncatedError(payload Request, reason string, usage modelcall.TokenUsage) error {
	return &failure.ProviderOutputTruncatedError{
		ProviderID:       p.id,
		Model:            payload.Model,
		FinishReason:     reason,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ContextTokens:    payload.Options.NumCtx,
	}
}

// projectPromptTokens learns a conservative context reservation.
func (p *Provider) projectPromptTokens(model string, estimated int) int {
	if estimated <= 0 {
		return 0
	}
	p.promptMu.Lock()
	scale := p.promptScaleMillis[model]
	p.promptMu.Unlock()
	if scale == 0 {
		scale = ollamaInitialPromptScaleMillis
	}
	maxInt := int(^uint(0) >> 1)
	if estimated > (maxInt-999)/scale {
		return maxInt
	}
	return (estimated*scale + 999) / 1000
}

// The largest observed ratio plus ten percent headroom bounds future reservations.
func (p *Provider) observePromptTokens(model string, estimated, reported int) {
	if estimated <= 0 || reported <= 0 {
		return
	}
	scale64 := (int64(reported)*1100 + int64(estimated) - 1) / int64(estimated)
	if scale64 > ollamaMaxPromptScaleMillis {
		scale64 = ollamaMaxPromptScaleMillis
	}
	scale := int(scale64)
	if scale < 1000 {
		scale = 1000
	}
	p.promptMu.Lock()
	if scale > p.promptScaleMillis[model] {
		p.promptScaleMillis[model] = scale
	}
	p.promptMu.Unlock()
}

// warnIfTruncated reports truncation when the context ceiling was unknown.
func (p *Provider) warnIfTruncated(payload Request, promptEstimate, reportedPromptTokens int) {
	if reportedPromptTokens <= 0 || promptEstimate <= 0 {
		return
	}
	if reportedPromptTokens >= payload.Options.NumCtx && reportedPromptTokens < promptEstimate {
		slog.Warn("ollama prompt likely truncated to context window",
			"component", "ollama_provider",
			"provider", p.id,
			"model", payload.Model,
			"num_ctx", payload.Options.NumCtx,
			"prompt_tokens_estimated", promptEstimate,
			"prompt_tokens_processed", reportedPromptTokens)
	}
}

var _ modelcall.Provider = (*Provider)(nil)
