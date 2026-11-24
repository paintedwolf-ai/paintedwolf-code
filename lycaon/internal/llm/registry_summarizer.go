package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/pkg/api"
)

const summaryRequestHeadroom = 1024

// RegistrySummarizer uses a Provider from Registry for lite summarization.
type RegistrySummarizer struct {
	Registry   *Registry
	Policy     *PolicyStore
	Scope      SettingsScope
	ProjectID  string
	ProjectDir string
	Fallback   compaction.Summarizer
	// Cost records utility-call usage.
	Cost cost.CostTracker
	// Purpose labels utility calls.
	Purpose string
	// Class controls fallback behavior.
	Class UtilityClass
	// Plane coordinates utility calls.
	Plane *UtilityPlane
	// Refusals stores host-declined pairs.
	Refusals *providerretry.ModelRefusalGate
	// Lifecycle publishes occupied calls.
	Lifecycle CallLifecycle

	curatorCacheMu sync.RWMutex
	curatorCache   map[string]CurationResult
}

// SummarizeRequired excludes local truncation fallbacks.
func (r *RegistrySummarizer) SummarizeRequired(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (string, error) {
	return r.summarize(ctx, systemPrompt, userPrompt, maxTokens, false)
}

func (r *RegistrySummarizer) utilityClass() UtilityClass {
	if r != nil && r.Class != "" {
		return r.Class
	}
	if r == nil {
		return UtilityClassQuality
	}
	return ClassForPurpose(r.Purpose)
}

func (r *RegistrySummarizer) summarize(ctx context.Context, systemPrompt, userPrompt string, maxTokens int, allowFallback bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	class := r.utilityClass()
	p, model, err := r.liteProvider(ctx)
	if err != nil || p == nil {
		if ctx.Err() != nil || SecretScreenBlocked(err) {
			return "", preserveCallerContextError(err, ctx.Err())
		}
		if class.AllowsCoordinatorFallback() {
			if cp, cmodel, cerr := r.coordinatorProvider(ctx); cerr == nil && cp != nil {
				content, coordErr := r.summarizeWith(ctx, cp, cmodel, systemPrompt, userPrompt, maxTokens)
				if coordErr == nil {
					return content, nil
				}
				err = newSummarizerFallbackError("", "", err, cp.ID(), cmodel, coordErr)
			}
		}
		return r.degradeSummarize(ctx, systemPrompt, userPrompt, maxTokens, allowFallback, class, err)
	}
	var liteErr error
	if r.Plane != nil && !r.Plane.allowLite() {
		liteErr = ErrLiteUnavailable
	} else {
		var content string
		content, liteErr = r.summarizeWith(ctx, p, model, systemPrompt, userPrompt, maxTokens)
		if liteErr == nil {
			return content, nil
		}
	}
	slog.WarnContext(ctx, "lite summarize failed",
		"purpose", r.Purpose, "class", class, "provider", p.ID(), "model", model, "error", liteErr)
	// Screened bytes and expired callers never advance to fallback providers or truncation.
	if SecretScreenBlocked(liteErr) || ctx.Err() != nil {
		return "", preserveCallerContextError(liteErr, ctx.Err())
	}
	cause := liteErr
	if class.AllowsCoordinatorFallback() {
		if cp, cmodel, cerr := r.coordinatorProvider(ctx); cerr == nil && cp != nil && (cp.ID() != p.ID() || cmodel != model) && ctx.Err() == nil {
			content, coordErr := r.summarizeWith(ctx, cp, cmodel, systemPrompt, userPrompt, maxTokens)
			if coordErr == nil {
				return content, nil
			}
			slog.WarnContext(ctx, "coordinator summarize fallback failed",
				"purpose", r.Purpose, "provider", cp.ID(), "model", cmodel, "error", coordErr)
			cause = newSummarizerFallbackError(p.ID(), model, liteErr, cp.ID(), cmodel, coordErr)
			if SecretScreenBlocked(coordErr) || ctx.Err() != nil {
				return "", preserveCallerContextError(cause, ctx.Err())
			}
		}
	}
	return r.degradeSummarize(ctx, systemPrompt, userPrompt, maxTokens, allowFallback, class, cause)
}

func (r *RegistrySummarizer) degradeSummarize(ctx context.Context, systemPrompt, userPrompt string, maxTokens int, allowFallback bool, class UtilityClass, cause error) (string, error) {
	fail := func() (string, error) {
		if cause != nil {
			return "", cause
		}
		return "", newUnconfiguredUtilityError("no configured provider")
	}
	if ctx.Err() != nil || SecretScreenBlocked(cause) || class.ReturnsFailureToCaller() || !allowFallback {
		return fail()
	}
	if class.AllowsTruncateFallback() {
		if r.Fallback != nil {
			return r.Fallback.Summarize(ctx, systemPrompt, userPrompt, maxTokens)
		}
		return compaction.TruncateSummarizer{}.Summarize(ctx, systemPrompt, userPrompt, maxTokens)
	}
	// Only truncation-enabled classes may return excerpts as summaries.
	if r.Fallback != nil && !isTruncatingFallback(r.Fallback) {
		return r.Fallback.Summarize(ctx, systemPrompt, userPrompt, maxTokens)
	}
	return fail()
}

// answerTokens keeps maxTokens for the answer on a model that cannot turn
// reasoning off: its cap also covers reasoning, which runs at least at low
// effort, so that allowance is added.
func (r *RegistrySummarizer) answerTokens(ctx context.Context, p modelcall.Provider, model string, maxTokens int) int {
	if maxTokens <= 0 || r.Registry == nil {
		return maxTokens
	}
	entry, _ := r.Registry.thinkingModelEntry(ctx, p.ID(), model)
	if !modelcall.ResolveModelThinking(p.Profile(), entry, model).AlwaysOn {
		return maxTokens
	}
	allowance, _ := modelcall.ThinkLow.AnthropicBudget()
	return maxTokens + allowance
}

func (r *RegistrySummarizer) summarizeWith(ctx context.Context, p modelcall.Provider, model, systemPrompt, userPrompt string, maxTokens int) (string, error) {
	today, err := guidance.RenderTodayLine(ctx, time.Now(), ResolveModelCutoff(model))
	if err != nil {
		return "", err
	}
	rawMessages := []api.Message{
		{Role: api.MessageRoleSystem, Content: today + "\n\n" + systemPrompt, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted},
		{Role: api.MessageRoleUser, Content: userPrompt, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted},
	}
	req := modelcall.CompletionRequest{
		Model:    model,
		Messages: transcript.Project(rawMessages),
		Debug:    modelcall.RequestDebug{Purpose: r.Purpose},
		// Short utility outputs use the configured no-reasoning mode.
		Think:     modelcall.ThinkOff,
		MaxTokens: r.answerTokens(ctx, p, model, maxTokens),
	}
	maxTokens = req.MaxTokens
	if compaction.SummaryFormatRequested(ctx) {
		req.ResponseFormat = compaction.CompactionSummaryResponseFormat()
	}
	if window := r.Registry.ContextLength(p.ID(), model); window > 0 && compaction.SummaryFormatRequested(ctx) {
		encoded, err := json.Marshal(req.ResponseFormat)
		if err != nil {
			return "", fmt.Errorf("encode summary response format: %w", err)
		}
		estimated := modelcall.EstimateRequestInput(req) + tokenest.EstimateDefault(string(encoded))
		if estimated+maxTokens+summaryRequestHeadroom > window {
			return "", &failure.ProviderContextTooSmallError{ProviderID: p.ID(), Model: model, PromptTokens: estimated + summaryRequestHeadroom, CompletionReserve: maxTokens, MaxContext: window, Source: "summary-request-estimate"}
		}
	}
	resp, err := r.completeUtility(ctx, p, model, req)
	if err != nil && req.ResponseFormat != nil && providerretry.IsRequestRejected(err) {
		originalErr := err
		req.ResponseFormat = nil
		resp, err = r.completeUtility(ctx, p, model, req)
		if err != nil {
			return "", originalErr
		}
	}
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func (r *RegistrySummarizer) localModel(ctx context.Context) string {
	if r == nil || r.Registry == nil {
		return ""
	}
	return r.Registry.IncludedModel(ctx)
}

func (r *RegistrySummarizer) coordinatorProvider(ctx context.Context) (modelcall.Provider, string, error) {
	return r.slotProvider(ctx, func(p ModelPolicy) ModelRef {
		return CoordinatorRefWithLocalDefault(p, r.localModel(ctx))
	}, "coordinator model not configured")
}

func (r *RegistrySummarizer) liteProvider(ctx context.Context) (modelcall.Provider, string, error) {
	return r.slotProvider(ctx, func(p ModelPolicy) ModelRef {
		return SummarizerRefWithLocalDefault(p, r.localModel(ctx))
	}, "lite model not configured")
}

// slotProvider advances past standing model refusals.
func (r *RegistrySummarizer) slotProvider(ctx context.Context, pick func(ModelPolicy) ModelRef, missing string) (modelcall.Provider, string, error) {
	if r.Policy == nil || r.Registry == nil {
		return nil, "", newUnconfiguredUtilityError("registry not configured")
	}
	policy, err := r.modelPolicy(ctx)
	if err != nil {
		return nil, "", err
	}
	ref := pick(policy)
	if ref.ProviderID == "" {
		return nil, "", newUnconfiguredUtilityError(missing)
	}
	p, err := r.Registry.Get(ref.ProviderID)
	if err != nil {
		return nil, "", err
	}
	model := ref.Model
	if model == "" && len(p.Models()) > 0 {
		model = p.Models()[0].ID
	}
	if refused := r.Refusals.Err(ref.ProviderID, model); refused != nil {
		return nil, "", refused
	}
	return p, model, nil
}

// isTruncatingFallback identifies summaries made from input excerpts.
func isTruncatingFallback(s compaction.Summarizer) bool {
	switch s.(type) {
	case compaction.TruncateSummarizer, *compaction.TruncateSummarizer:
		return true
	default:
		return false
	}
}

// Summarize calls the configured utility model.
func (r *RegistrySummarizer) Summarize(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (string, error) {
	return r.summarize(ctx, systemPrompt, userPrompt, maxTokens, true)
}
