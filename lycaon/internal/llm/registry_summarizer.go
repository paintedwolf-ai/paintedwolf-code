package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/pkg/api"
)

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
	class := r.utilityClass()
	p, model, err := r.liteProvider()
	if err != nil || p == nil {
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
	// Screened bytes stay held across provider and truncation fallbacks.
	if SecretScreenBlocked(liteErr) {
		return "", liteErr
	}
	if class.AllowsCoordinatorFallback() {
		if cp, cmodel, cerr := r.coordinatorProvider(); cerr == nil && cp != nil && (cp.ID() != p.ID() || cmodel != model) {
			content, coordErr := r.summarizeWith(ctx, cp, cmodel, systemPrompt, userPrompt, maxTokens)
			if coordErr == nil {
				return content, nil
			}
			slog.WarnContext(ctx, "coordinator summarize fallback failed",
				"purpose", r.Purpose, "provider", cp.ID(), "model", cmodel, "error", coordErr)
		}
	}
	return r.degradeSummarize(ctx, systemPrompt, userPrompt, maxTokens, allowFallback, class, liteErr)
}

func (r *RegistrySummarizer) degradeSummarize(ctx context.Context, systemPrompt, userPrompt string, maxTokens int, allowFallback bool, class UtilityClass, cause error) (string, error) {
	fail := func() (string, error) {
		if cause != nil {
			return "", cause
		}
		return "", fmt.Errorf("no configured provider")
	}
	if class.ReturnsFailureToCaller() || !allowFallback {
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

func (r *RegistrySummarizer) summarizeWith(ctx context.Context, p modelcall.Provider, model, systemPrompt, userPrompt string, maxTokens int) (string, error) {
	today, err := guidance.RenderTodayLine(ctx, time.Now(), ResolveModelCutoff(model))
	if err != nil {
		return "", err
	}
	req := modelcall.CompletionRequest{
		Model: model,
		Messages: transcript.Project([]api.Message{
			{Role: api.MessageRoleSystem, Content: today + "\n\n" + systemPrompt, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted},
			{Role: api.MessageRoleUser, Content: userPrompt, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted},
		}),
		Debug: modelcall.RequestDebug{Purpose: r.Purpose},
		// Short utility outputs use the configured no-reasoning mode.
		Think:     modelcall.ThinkOff,
		MaxTokens: maxTokens,
	}
	if compaction.SummaryFormatRequested(ctx) {
		req.ResponseFormat = compaction.CompactionSummaryResponseFormat()
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

func (r *RegistrySummarizer) coordinatorProvider() (modelcall.Provider, string, error) {
	return r.slotProvider(CoordinatorRef, "coordinator model not configured")
}

func (r *RegistrySummarizer) liteProvider() (modelcall.Provider, string, error) {
	return r.slotProvider(SummarizerRef, "lite model not configured")
}

// slotProvider advances past standing model refusals.
func (r *RegistrySummarizer) slotProvider(pick func(ModelPolicy) ModelRef, missing string) (modelcall.Provider, string, error) {
	if r.Policy == nil || r.Registry == nil {
		return nil, "", fmt.Errorf("registry not configured")
	}
	scope := r.Scope
	if scope == "" {
		scope = SettingsScopeGlobal
	}
	policy, err := r.Policy.Get(scope, r.ProjectDir)
	if err != nil {
		return nil, "", err
	}
	ref := pick(policy)
	if ref.ProviderID == "" {
		return nil, "", errors.New(missing)
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

// SummaryRevision identifies the routed utility policy that plans summaries.
func (r *RegistrySummarizer) SummaryRevision() (string, error) {
	if r.Policy == nil {
		return "local", nil
	}
	scope := r.Scope
	if scope == "" {
		scope = SettingsScopeGlobal
	}
	policy, err := r.Policy.Get(scope, r.ProjectDir)
	if err != nil {
		return "", err
	}
	return compaction.RevisionDigest(struct {
		Policy  ModelPolicy
		Class   UtilityClass
		Purpose string
	}{policy, r.utilityClass(), r.Purpose})
}
