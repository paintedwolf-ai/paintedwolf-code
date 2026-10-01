// Package openaicompat implements chat-completion transports and their endpoint-specific
// authentication, reasoning, and streaming behavior.
package openaicompat

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerauth"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/tools"
	openai "github.com/sashabaranov/go-openai"
)

// Provider sends chat completion requests.
type Provider struct {
	id             string
	baseURL        string
	apiKey         string
	profile        providerprofile.Profile
	extraHeaders   map[string]string
	http           httpProfile
	streamClient   *http.Client
	completeClient *http.Client
	// httpRetry is the per-provider chat HTTP retry policy from providers.yaml.
	httpRetry providerretry.ProviderHTTPRetry
	// tokenSource mints per-request credentials.
	tokenSource func(context.Context) (string, error)
	modelcall.Catalog
}

// httpProfile configures request authentication and token fields.
type httpProfile struct {
	authHeader string // header carrying the credential; "Authorization" by default
	authScheme string // prefix before the key; "Bearer " by default
	// maxCompletionTokens selects the reasoning completion cap field.
	maxCompletionTokens bool
}

func defaultHTTPProfile() httpProfile {
	return httpProfile{authHeader: "Authorization", authScheme: "Bearer "}
}

func OpenAIHTTPProfile() httpProfile {
	profile := defaultHTTPProfile()
	profile.maxCompletionTokens = true
	return profile
}

// WithHTTPProfile overrides request authentication and fields.
func (p *Provider) WithHTTPProfile(profile httpProfile) *Provider {
	if profile.authHeader == "" {
		profile.authHeader = "Authorization"
	}
	p.http = profile
	return p
}

// WithTokenSource sets a per-request credential source.
func (p *Provider) WithTokenSource(src func(context.Context) (string, error)) *Provider {
	p.tokenSource = src
	return p
}

// New constructs a chat completion provider.
func New(id, baseURL, apiKey string, models []modelinfo.Entry) *Provider {
	return &Provider{
		id:             id,
		baseURL:        strings.TrimRight(baseURL, "/"),
		apiKey:         apiKey,
		Catalog:        modelcall.NewCatalog("", models),
		profile:        providerprofile.OpenAI(),
		http:           defaultHTTPProfile(),
		streamClient:   providerhttp.NewStreamingClient(httpclient.DefaultResponseHeader),
		completeClient: providerhttp.NewStreamingClient(providerprofile.HostedCompleteResponseHeaderTimeout),
	}
}

// WithProfile overrides the driver profile.
func (p *Provider) WithProfile(profile providerprofile.Profile) *Provider {
	p.profile = profile
	if profile.StreamResponseHeaderTimeout > 0 {
		p.streamClient = providerhttp.NewStreamingClient(profile.StreamResponseHeaderTimeout)
	}
	if profile.CompleteResponseHeaderTimeout > 0 {
		p.completeClient = providerhttp.NewStreamingClient(profile.CompleteResponseHeaderTimeout)
	}
	return p
}

// WithExtraHeaders sets optional HTTP headers on chat completion requests.
func (p *Provider) WithExtraHeaders(headers map[string]string) *Provider {
	if len(headers) == 0 {
		return p
	}
	p.extraHeaders = make(map[string]string, len(headers))
	for k, v := range headers {
		p.extraHeaders[k] = v
	}
	return p
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

// WithEffectiveModels replaces the provider's model catalog.
func (p *Provider) WithEffectiveModels(models []modelinfo.Entry) *Provider {
	next := *p
	next.Catalog = p.WithModels(models)
	return &next
}

func (p *Provider) ID() string { return p.id }

func (p *Provider) Profile() providerprofile.Profile { return p.profile }

func (p *Provider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return p.completeWithGuards(ctx, req, controlOpts{})
}

func (p *Provider) completeWithGuards(ctx context.Context, req modelcall.CompletionRequest, opts controlOpts) (*modelcall.Completion, error) {
	body, err := encodeChatCompletionRequest(req, p, false, opts)
	if err != nil {
		return nil, err
	}
	req.ControlCapture.Record(body)
	resp, err := p.postChatCompletion(ctx, p.ResolveModel(req), body, p.promptCacheRequestHeaders(req))
	if err != nil {
		if retryOut, retryErr, ok := p.retryCompleteAfterProviderError(ctx, req, opts, err); ok {
			return retryOut, retryErr
		}
		return nil, err
	}
	out := mapChatCompletionWire(resp)
	if out == nil {
		return nil, &failure.ProviderEmptyCompletionError{
			ProviderID: p.id,
			Model:      p.ResolveModel(req),
			Retryable:  true,
			Reason:     "empty choices",
		}
	}
	if !modelcall.CompletionHasPayload(out) {
		reason := string(resp.Choices[0].FinishReason)
		return out, &failure.ProviderEmptyCompletionError{
			ProviderID: p.id,
			Model:      p.ResolveModel(req),
			Retryable:  EmptyCompletionRetryable(reason),
			Reason:     reason,
			Terminal:   reason != "",
		}
	}
	return out, modelcall.OutputTruncation(req, p.id, p.ResolveModel(req), string(resp.Choices[0].FinishReason), out.Usage, len(out.ToolCalls) > 0)
}

func (p *Provider) retryCompleteAfterProviderError(ctx context.Context, req modelcall.CompletionRequest, opts controlOpts, err error) (*modelcall.Completion, error, bool) {
	if req.ThinkingOverride != nil {
		return nil, err, false
	}
	model := p.ResolveModel(req)
	current := p.reasoningFallbackFor(model, opts)
	if current == reasoningFallbackNone && !p.requestHasReasoningControl(req, model, opts) {
		return nil, err, false
	}
	next, ok := nextReasoningFallback(err, current, p.profile.ReasoningEffortOff != "")
	if !ok {
		return nil, err, false
	}
	retryOpts := controlOpts{
		strictRetry: opts.strictRetry,
		reasoning:   next,
	}
	out, retryErr := p.completeWithGuards(ctx, req, retryOpts)
	if retryErr != nil {
		return nil, err, true
	}
	markReasoningFallback(p.id, req.Model, next)
	return out, nil, true
}

func (p *Provider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	return p.streamAttempt(ctx, req, controlOpts{})
}

func (p *Provider) streamFromJSONResponse(ctx context.Context, req modelcall.CompletionRequest, resp *http.Response) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk, 2)
	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()
		var wire chatCompletionResponseWire
		if err := providerhttp.DecodeCompletionResponse("openai-compatible", resp, &wire); err != nil {
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: fmt.Errorf("openai-compatible: decode non-SSE stream response: %w", err), Done: true})
			return
		}
		completion := mapChatCompletionWire(&wire)
		if completion == nil {
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: &failure.ProviderEmptyCompletionError{
				ProviderID: p.id,
				Model:      p.ResolveModel(req),
				Retryable:  true,
				Reason:     "empty choices",
			}, Usage: NormalizeUsage(wire.Usage), Done: true})
			return
		}
		if completion.Reasoning != "" {
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Reasoning: completion.Reasoning})
		}
		if !modelcall.CompletionHasPayload(completion) {
			reason := string(wire.Choices[0].FinishReason)
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Err: &failure.ProviderEmptyCompletionError{
				ProviderID: p.id,
				Model:      p.ResolveModel(req),
				Retryable:  EmptyCompletionRetryable(reason),
				Reason:     reason,
				Terminal:   reason != "",
			}, Usage: completion.Usage, Done: true})
			return
		}
		modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{
			Content: completion.Content, ReasoningDetails: completion.ReasoningDetails,
			ToolCalls: completion.ToolCalls, Usage: completion.Usage, Done: true,
			Err: modelcall.OutputTruncation(req, p.id, p.ResolveModel(req), string(wire.Choices[0].FinishReason), completion.Usage, len(completion.ToolCalls) > 0),
		})
	}()
	return ch, nil
}

func EmptyCompletionRetryable(reason string) bool {
	switch openai.FinishReason(strings.TrimSpace(reason)) {
	case "", openai.FinishReasonStop,
		openai.FinishReasonFunctionCall, openai.FinishReasonToolCalls,
		openai.FinishReasonNull:
		return true
	default:
		return false
	}
}

func ProjectTools(toolMetas []tools.ToolMeta) []openai.Tool {
	out := make([]openai.Tool, 0, len(toolMetas))
	for _, raw := range toolMetas {
		t := providerwire.ProjectToolMetaForModel(raw)
		fn := openai.FunctionDefinition{
			Name:        t.Name,
			Description: t.Description,
		}
		if t.ArgsSchema != nil {
			fn.Parameters = t.ArgsSchema
		} else {
			fn.Parameters = map[string]any{"type": "object"}
		}
		out = append(out, openai.Tool{
			Type:     openai.ToolTypeFunction,
			Function: &fn,
		})
	}
	return out
}

var _ modelcall.Provider = (*Provider)(nil)

// AzureHTTPProfile configures the v1 data-plane contract.
func AzureHTTPProfile() httpProfile {
	return httpProfile{
		authHeader:          "api-key",
		authScheme:          "",
		maxCompletionTokens: true,
	}
}

// WithReasoningWire sets the assistant-history format accepted by this endpoint.
func (p *Provider) WithReasoningWire(style providerprofile.ReasoningWireStyle) *Provider {
	p.profile.ReasoningWire = style
	return p
}

// NewVertex constructs the Vertex AI chat-completions provider on ambient
// Google credentials and the configured project region.
func NewVertex(ctx context.Context, id string, models []modelinfo.Entry) modelcall.Provider {
	probeCtx, cancel := context.WithTimeout(ctx, discovery.VertexTimeout)
	defer cancel()
	project, _ := providerauth.VertexProject(probeCtx)
	region := providerauth.VertexRegion()
	baseURL := fmt.Sprintf(
		"https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/endpoints/openapi",
		region, project, region,
	)
	return New(id, baseURL, "", models).
		WithProfile(providerprofile.Vertex()).
		WithTokenSource(providerauth.NewVertexTokenSource())
}
