// Package bedrock implements the Bedrock Converse protocol and its SDK fault handling.
package bedrock

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerauth"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/tools"
)

// Provider calls the Converse API.
type Provider struct {
	id        string
	region    string
	apiKey    string
	profile   providerprofile.Profile
	httpRetry providerretry.ProviderHTTPRetry
	modelcall.Catalog

	clientMu   sync.Mutex
	client     *bedrockruntime.Client
	loadConfig func(context.Context, string) (aws.Config, error)
}

// New constructs a provider from explicit or ambient credentials.
func New(id, region, apiKey string, models []modelinfo.Entry) *Provider {
	profile := providerprofile.OpenAI()
	profile.Streaming = providerprofile.StreamFanout
	profile.Thinking = modelinfo.ThinkStyleNone
	// This transport emits no reasoning controls.
	profile.ThinkingStyles = nil
	profile.Discovery = providerprofile.DiscoveryBedrock
	profile.ToolCallID = providerprofile.ToolCallIDWireRoundTrip
	profile.CompleteResponseHeaderTimeout = providerprofile.HostedCompleteResponseHeaderTimeout
	return &Provider{
		id:         id,
		region:     strings.TrimSpace(region),
		apiKey:     strings.TrimSpace(apiKey),
		Catalog:    modelcall.NewCatalog("bedrock", models),
		profile:    profile,
		loadConfig: providerauth.LoadBedrockConfig,
	}
}

func (p *Provider) ID() string                       { return p.id }
func (p *Provider) Profile() providerprofile.Profile { return p.profile }

// WithEffectiveModels returns a fresh runtime instance.
func (p *Provider) WithEffectiveModels(models []modelinfo.Entry) *Provider {
	next := New(p.id, p.region, p.apiKey, models)
	next.profile = p.profile
	next.httpRetry = p.httpRetry.Clone()
	next.loadConfig = p.loadConfig
	return next
}

// WithHTTPRetry sets the chat retry policy.
func (p *Provider) WithHTTPRetry(policy providerretry.ProviderHTTPRetry) *Provider {
	p.httpRetry = policy.Clone()
	return p
}

// WithPromptCache sets the provider's prompt-cache policy.
func (p *Provider) WithPromptCache(policy providerprofile.PromptCachePolicy) *Provider {
	p.profile.PromptCache = policy
	return p
}

func (p *Provider) ensureClient(ctx context.Context) (*bedrockruntime.Client, error) {
	p.clientMu.Lock()
	defer p.clientMu.Unlock()
	if p.client != nil {
		return p.client, nil
	}
	loader := p.loadConfig
	if loader == nil {
		loader = providerauth.LoadBedrockConfig
	}
	cfg, err := loader(ctx, p.region)
	if err != nil {
		return nil, err
	}
	p.client = bedrockruntime.NewFromConfig(cfg, p.runtimeOptions()...)
	return p.client, nil
}

// runtimeOptions sends Converse through the host's provider transport under the
// non-streaming header bound. SDK retries are off so each attempt is one
// observed request.
func (p *Provider) runtimeOptions() []func(*bedrockruntime.Options) {
	return append(providerauth.BedrockRuntimeOptions(p.apiKey), func(o *bedrockruntime.Options) {
		o.HTTPClient = providerhttp.NewStreamingClient(p.profile.CompleteResponseHeaderTimeout)
		o.Retryer = aws.NopRetryer{}
	})
}

func (p *Provider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	client, err := p.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	model := p.ResolveModel(req)
	input := p.buildConverseInput(req, model)
	var lastErr error
	var fault providerretry.Fault
	var attempts int
	retries := 0
	for attempt := 0; ; attempt++ {
		attempts = attempt + 1
		admission, admitErr := (providerretry.ProviderAttempt{ProviderID: p.id, Model: model, Policy: p.httpRetry}).Admission(ctx)
		if admitErr != nil {
			return nil, admitErr
		}
		obsCtx, readObs := httpclient.Observe(ctx)
		started := time.Now()
		out, err := client.Converse(obsCtx, input)
		if err == nil {
			if err := admission.Succeeded(ctx); err != nil {
				return nil, err
			}
			completion := mapConverseOutput(out)
			if modelcall.CompletionHasPayload(completion) {
				return completion, modelcall.OutputTruncation(req, p.id, model, string(out.StopReason), completion.Usage, len(completion.ToolCalls) > 0)
			}
			reason := ""
			if out != nil {
				reason = string(out.StopReason)
			}
			return completion, &failure.ProviderEmptyCompletionError{
				ProviderID: p.id,
				Model:      model,
				Retryable:  bedrockEmptyCompletionRetryable(out),
				Reason:     reason,
				Terminal:   reason != "",
			}
		}
		lastErr = err
		var sent bool
		fault, sent = classifyConverse(ctx, readObs(), time.Since(started), err)
		if fault.Kind == providerretry.FaultCanceled {
			return nil, fault.Err
		}
		if !sent {
			return nil, fmt.Errorf("bedrock: converse: %w", err)
		}
		if refusal := bedrockModelRefusal(p.id, model, err); refusal != nil {
			return nil, refusal
		}
		if fault.Kind == providerretry.FaultRateLimited && admission != nil {
			if err := admission.Limited(ctx, nil, p.httpRetry); err != nil {
				return nil, err
			}
			if p.httpRetry.RateLimit.Adaptive {
				continue
			}
		}
		if !providerretry.ShouldRetryFault(p.httpRetry, retries, fault) {
			break
		}
		if waitErr := providerretry.AwaitFaultRetry(ctx, p.httpRetry, retries, fault); waitErr != nil {
			return nil, waitErr
		}
		retries++
	}
	if fault.Transport() {
		return nil, providerretry.TransportFaultError(p.id, model, fault, attempts)
	}
	wrapped := fmt.Errorf("bedrock: converse: %w", lastErr)
	return nil, providerretry.ExhaustedHTTPError(p.id, model, fault.Status, attempts, wrapped)
}

// classifyConverse maps a Converse failure onto providerretry's fault kinds:
// caller cancellation first, then the answered status, then delivery state for
// a failed send. sent is false when the SDK failed before sending.
func classifyConverse(ctx context.Context, obs httpclient.RequestObservation, elapsed time.Duration, err error) (fault providerretry.Fault, sent bool) {
	if ctx.Err() != nil {
		return providerretry.ClassifyAttempt(ctx, nil, obs, elapsed, err), true
	}
	if bedrockAnswered(err) {
		fault = providerretry.FaultForStatus(bedrockAnsweredStatus(err), err)
		fault.Elapsed = elapsed
		return fault, true
	}
	var sendErr *smithyhttp.RequestSendError
	var responseErr *smithyhttp.ResponseError
	if errors.As(err, &sendErr) || errors.As(err, &responseErr) {
		return providerretry.ClassifyAttempt(ctx, nil, obs, elapsed, err), true
	}
	return providerretry.Fault{}, false
}

// bedrockAnswered reports whether the service sent an HTTP response.
func bedrockAnswered(err error) bool {
	return bedrockAnsweredStatus(err) != 0
}

// bedrockAnsweredStatus maps an answered fault onto the status the retry policy
// reads: typed throttling and service faults first, then the response status.
func bedrockAnsweredStatus(err error) int {
	if status := bedrockRetryStatus(err); status != 0 {
		return status
	}
	return bedrockResponseStatus(err)
}

func bedrockEmptyCompletionRetryable(out *bedrockruntime.ConverseOutput) bool {
	if out == nil || out.StopReason == "" {
		return true
	}
	switch out.StopReason {
	case brtypes.StopReasonEndTurn,
		brtypes.StopReasonToolUse,
		brtypes.StopReasonStopSequence,
		brtypes.StopReasonMalformedModelOutput,
		brtypes.StopReasonMalformedToolUse:
		return true
	default:
		return false
	}
}

// Stream fans the single Converse response out as one terminal chunk. The sends
// never block: every path sends at most twice into a channel buffered for two.
func (p *Provider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk, 2)
	go func() {
		defer close(ch)
		completion, err := p.Complete(ctx, req)
		if err != nil {
			terminal := modelcall.StreamChunk{Err: err, Done: true}
			if completion != nil {
				terminal.Usage = completion.Usage
				terminal.Reasoning = completion.Reasoning
				terminal.Content = completion.Content
			}
			ch <- terminal
			return
		}
		if completion.Reasoning != "" {
			ch <- modelcall.StreamChunk{Reasoning: completion.Reasoning}
		}
		if len(completion.ToolCalls) > 0 {
			ch <- modelcall.StreamChunk{ToolCalls: completion.ToolCalls, Usage: completion.Usage, Done: true}
			return
		}
		ch <- modelcall.StreamChunk{Content: completion.Content, Usage: completion.Usage, Done: true}
	}()
	return ch, nil
}

func (p *Provider) buildConverseInput(req modelcall.CompletionRequest, model string) *bedrockruntime.ConverseInput {
	entry, _ := p.ModelEntry(model)
	vision := modelinfo.Supported(entry.EffectiveCapabilities().Vision)
	req.Messages = providerwire.PrepareMessagesForVision(req.Messages, vision, req.Debug.SessionID)
	// The model family's policy decides whether checkpoints are sent.
	req.Model = model
	proj := providerwire.ProjectPromptCache(req, p.profile.PromptCache, entry.EffectiveCapabilities())
	checkpoints := providerwire.TrailingPromptCacheBreakpoints(proj.Breakpoints, providerwire.BedrockCacheCheckpointLimit)
	system, messages := ProjectMessages(req.Messages, checkpoints, vision, req.Debug.SessionID)

	var temperature *float32
	if entry.ID != "" {
		if entry.Temperature != nil {
			t := float32(*entry.Temperature)
			temperature = &t
		}
	}
	maxTokens := modelcall.CompletionTokenLimit(req, entry.MaxTokens, providerprofile.DefaultAnthropicMaxTokens, modelcall.ThinkOff, false)
	req.RecordBudget(maxTokens, nil, nil)

	input := &bedrockruntime.ConverseInput{
		ModelId:  aws.String(model),
		Messages: messages,
		InferenceConfig: &brtypes.InferenceConfiguration{
			MaxTokens:   aws.Int32(int32MaxTokens(maxTokens)),
			Temperature: temperature,
		},
	}
	if len(system) > 0 {
		input.System = system
	}
	// Converse has no no-call tool choice and requires the tool configuration
	// whenever history holds tool blocks, so a request that forbids tool use
	// keeps its definitions and the host refuses any call it returns.
	if tcfg := bedrockToolConfig(req.Tools); tcfg != nil {
		input.ToolConfig = tcfg
	}
	return input
}

func bedrockToolConfig(metas []tools.ToolMeta) *brtypes.ToolConfiguration {
	if len(metas) == 0 {
		return nil
	}
	out := make([]brtypes.Tool, 0, len(metas))
	for _, t := range metas {
		schema := t.ArgsSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		out = append(out, &brtypes.ToolMemberToolSpec{
			Value: brtypes.ToolSpecification{
				Name:        aws.String(t.Name),
				Description: aws.String(t.Description),
				InputSchema: &brtypes.ToolInputSchemaMemberJson{
					Value: document.NewLazyDocument(schema),
				},
			},
		})
	}
	return &brtypes.ToolConfiguration{Tools: out}
}

// bedrockModelRefusal reports a typed Converse fault naming a model this
// identity cannot invoke. Only AccessDenied is remembered: ResourceNotFound also
// covers an unsubmitted use-case form, which clears outside this process.
// ValidationException is excluded because it also covers a bad request body.
func bedrockModelRefusal(providerID, model string, err error) error {
	code, refused := bedrockRefusalCode(err)
	if !refused {
		return nil
	}
	return &providerretry.ModelRefusedError{
		ProviderID: providerID,
		Model:      model,
		Status:     bedrockResponseStatus(err),
		Code:       code,
		Evidence:   bedrockRefusalEvidence(err),
		Detail:     fmt.Sprintf("bedrock: converse: %v", err),
	}
}

// bedrockRefusalEvidence grades what the typed fault proved. Only AccessDenied
// names the model itself.
func bedrockRefusalEvidence(err error) providerretry.ModelRefusalEvidence {
	var denied *brtypes.AccessDeniedException
	if errors.As(err, &denied) {
		return providerretry.RefusalEvidenceModelIdentity
	}
	return providerretry.RefusalEvidenceInconclusive
}

func bedrockRefusalCode(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var notFound *brtypes.ResourceNotFoundException
	if errors.As(err, &notFound) {
		return "ResourceNotFoundException", true
	}
	var denied *brtypes.AccessDeniedException
	if errors.As(err, &denied) {
		return "AccessDeniedException", true
	}
	return "", false
}

// bedrockResponseStatus recovers the HTTP status when the SDK wrapped one, or 0.
func bedrockResponseStatus(err error) int {
	var responseErr *smithyhttp.ResponseError
	if errors.As(err, &responseErr) {
		return responseErr.HTTPStatusCode()
	}
	return 0
}

// bedrockRetryStatus maps typed Converse faults onto the statuses the
// http_retry allowlist understands (429 throttle, 503 unavailable), or 0 when
// no typed fault applies.
func bedrockRetryStatus(err error) int {
	if err == nil {
		return 0
	}
	var throttled *brtypes.ThrottlingException
	if errors.As(err, &throttled) {
		return http.StatusTooManyRequests
	}
	var unavailable *brtypes.ServiceUnavailableException
	if errors.As(err, &unavailable) {
		return http.StatusServiceUnavailable
	}
	var internal *brtypes.InternalServerException
	if errors.As(err, &internal) {
		return http.StatusServiceUnavailable
	}
	var responseErr *smithyhttp.ResponseError
	if errors.As(err, &responseErr) {
		switch responseErr.HTTPStatusCode() {
		case http.StatusTooManyRequests:
			return http.StatusTooManyRequests
		case http.StatusServiceUnavailable:
			return http.StatusServiceUnavailable
		}
	}
	return 0
}

// int32MaxTokens narrows a token budget to the int32 the Converse API expects,
// bounded to a positive value within int32 range.
func int32MaxTokens(v int) int32 {
	if v <= 0 || v > math.MaxInt32 {
		return providerprofile.DefaultAnthropicMaxTokens
	}
	return int32(v)
}

var _ modelcall.Provider = (*Provider)(nil)
