package llm

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ModelRole identifies which model_policy slot resolved a selection.
type ModelRole string

const (
	ModelRoleCoordinator ModelRole = "coordinator"
	ModelRoleLite        ModelRole = "lite"
	ModelRolePool        ModelRole = "pool"
)

// ModelSelection is the chosen provider/model pair.
type ModelSelection struct {
	ProviderID string
	Model      string
	Role       ModelRole
	// Fallback marks a selection served by the injected mock or manual client
	// under LYCAON_LLM_MOCK / manual mode, not by the resolved provider.
	Fallback bool
}

func userProvidersLocalPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "providers.local.yaml"), nil
}

func userModelPolicyPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, settingsoverlay.BasenameModelPolicy), nil
}

// privateConfigFileMode protects user configuration files.
const privateConfigFileMode = 0o600

// privateConfigDirMode protects user configuration directories.
const privateConfigDirMode = 0o700

// ContextLengthLookup returns a model's usable context window from a merged
// ModelEntry when known, without performing live HTTP discovery.
type ContextLengthLookup interface {
	ContextLength(provider, model string) int
}

// Resolve source labels for ResolveTrueWindow.
const (
	WindowResolveExplicit   = "explicit"
	WindowResolveModelEntry = "model_entry"
	WindowResolveMatchTable = "match_table"
	WindowResolveFallback   = "fallback"
)

// ResolveTrueWindow picks the model's physical usable context (true_window):
// an explicit window, then the model entry, the match table, and the fallback.
// No live HTTP.
func ResolveTrueWindow(policy ModelPolicy, lookup ContextLengthLookup, windows modelinfo.ModelContextWindows, cfg compaction.CompactionConfig) (trueWindow int, source string) {
	src := strings.TrimSpace(cfg.WindowSource)
	if src == "" {
		src = compaction.WindowSourceAuto
	}
	if src == compaction.WindowSourceExplicit && cfg.ModelContextWindow > 0 {
		return cfg.ModelContextWindow, WindowResolveExplicit
	}

	provider := strings.TrimSpace(policy.Coordinator.ProviderID)
	model := strings.TrimSpace(policy.Coordinator.Model)
	if lookup != nil && model != "" {
		if n := lookup.ContextLength(provider, model); n > 0 {
			return n, WindowResolveModelEntry
		}
	}
	if n, ok := windows.Lookup(model); ok && n > 0 {
		return n, WindowResolveMatchTable
	}
	fb := windows.FallbackTrueWindow
	if fb < 1 {
		fb = modelinfo.DefaultFallbackTrueWindow
	}
	return fb, WindowResolveFallback
}

// ApplyLiveBudget sets CompactionConfig.ModelContextWindow to the live budget
// (true_window × live_budget_pct, or the explicit window), re-derives token
// ceilings, and stamps MaxWorkerSummaryChars from ScaleLimitsFromTrueWindow
// unless an overlay already set a positive value.
func ApplyLiveBudget(cfg compaction.CompactionConfig, policy ModelPolicy, lookup ContextLengthLookup, windows modelinfo.ModelContextWindows) (compaction.CompactionConfig, SessionLimitFields, error) {
	trueWindow, _ := ResolveTrueWindow(policy, lookup, windows, cfg)

	overlayWorkerSummary := cfg.MaxWorkerSummaryChars

	src := strings.TrimSpace(cfg.WindowSource)
	if src == "" {
		src = compaction.WindowSourceAuto
	}
	if src == compaction.WindowSourceExplicit && cfg.ModelContextWindow > 0 {
		cfg.ModelContextWindow = trueWindow
	} else {
		pct := cfg.LiveBudgetPct
		if pct <= 0 {
			pct = compaction.DefaultLiveBudgetPct
		}
		cfg.ModelContextWindow = trueWindow * pct / 100
	}
	cfg.HardCeilingTokens = 0
	cfg.TargetTokens = 0
	normalized, err := compaction.NormalizeConfig(cfg)
	if err != nil {
		return compaction.CompactionConfig{}, SessionLimitFields{}, err
	}
	limits := ScaleLimitsFromTrueWindow(trueWindow)
	if overlayWorkerSummary > 0 {
		normalized.MaxWorkerSummaryChars = overlayWorkerSummary
	} else {
		normalized.MaxWorkerSummaryChars = limits.MaxWorkerSummaryChars
	}
	return normalized, limits, nil
}

// SessionLimitFields are session/compaction caps derived from a model's true_window.
type SessionLimitFields struct {
	MaxToolResultBytes       int
	MaxCoordinatorLoopCycles int
	MaxWorkerSummaryChars    int
}

// ScaleLimitsFromTrueWindow derives session/compaction limits from true_window.
// max(1, …) is arithmetic safety only — not a product minimum-window policy.
func ScaleLimitsFromTrueWindow(trueWindow int) SessionLimitFields {
	if trueWindow < 1 {
		trueWindow = 1
	}
	return SessionLimitFields{
		MaxToolResultBytes:       trueWindow * 2,
		MaxCoordinatorLoopCycles: max(1, trueWindow/1024),
		MaxWorkerSummaryChars:    max(1, trueWindow/16),
	}
}

// ErrCallBudgetExceeded is the cause a started budget cancels its call with.
var ErrCallBudgetExceeded = errors.New("provider call budget exceeded")

// CallBudget starts at provider dispatch, excludes screening time, and is shared across retries.
type CallBudget struct {
	limit   time.Duration
	mu      sync.Mutex
	started time.Time
	expired atomic.Bool
}

type callBudgetKey struct{}

// NewCallBudget returns the budget for one call. A limit of zero or less is
// unbounded: the call runs until its caller's context ends.
func NewCallBudget(limit time.Duration) *CallBudget {
	return &CallBudget{limit: limit}
}

// WithCallBudget attaches the budget the next provider call will start.
func WithCallBudget(ctx context.Context, budget *CallBudget) context.Context {
	if budget == nil {
		return ctx
	}
	return context.WithValue(ctx, callBudgetKey{}, budget)
}

func callBudgetFrom(ctx context.Context) *CallBudget {
	budget, _ := ctx.Value(callBudgetKey{}).(*CallBudget)
	return budget
}

// deadline starts the clock on the first send and holds that instant for every
// later attempt of the same call.
func (b *CallBudget) deadline() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.started.IsZero() {
		b.started = time.Now()
	}
	return b.started.Add(b.limit)
}

// startCallBudget converts an attached budget into a deadline on the outbound
// call. With no budget attached the context passes through unchanged.
func startCallBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	budget := callBudgetFrom(ctx)
	if budget == nil || budget.limit <= 0 {
		return ctx, func() {}
	}
	call, cancel := context.WithDeadlineCause(ctx, budget.deadline(), ErrCallBudgetExceeded)
	context.AfterFunc(call, func() {
		if errors.Is(context.Cause(call), ErrCallBudgetExceeded) {
			budget.expired.Store(true)
		}
	})
	return call, cancel
}

// Expired separates running out of budget from the caller's own cancellation.
func (b *CallBudget) Expired() bool {
	if b == nil {
		return false
	}
	if b.expired.Load() {
		return true
	}
	b.mu.Lock()
	started := b.started
	limit := b.limit
	b.mu.Unlock()
	return limit > 0 && !started.IsZero() && !time.Now().Before(started.Add(limit))
}

// budgetedProvider starts the call budget at the last host step before the
// request leaves, so every decorator above it runs on the caller's clock.
type budgetedProvider struct {
	inner modelcall.Provider
}

func (p *budgetedProvider) ID() string                       { return p.inner.ID() }
func (p *budgetedProvider) Models() []modelcall.ModelInfo    { return p.inner.Models() }
func (p *budgetedProvider) Profile() providerprofile.Profile { return p.inner.Profile() }

func (p *budgetedProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	req.CaptureSources()
	call, cancel := startCallBudget(ctx)
	defer cancel()
	return p.inner.Complete(call, req)
}

func (p *budgetedProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	req.CaptureSources()
	call, cancel := startCallBudget(ctx)
	ch, err := p.inner.Stream(call, req)
	if err != nil {
		cancel()
		return nil, err
	}
	// The stream outlives this call, so the deadline is released when the
	// caller's context ends rather than on return.
	context.AfterFunc(ctx, cancel)
	return ch, nil
}

// Providers report cumulative snapshots within one attempt. Retries add billable
// attempts, so consumers must see their combined usage, including failed turns.
type responseRetryUsage struct {
	completed modelcall.TokenUsage
	current   modelcall.TokenUsage
	attempt   modelcall.TokenUsage
}

func (u *responseRetryUsage) beginAttempt() {
	u.attempt = modelcall.TokenUsage{}
}

func (u *responseRetryUsage) observe(usage modelcall.TokenUsage, attempt *modelcall.TokenUsage) {
	if usage.Reported() {
		u.current = usage
	}
	if attempt != nil {
		u.attempt = *attempt
	} else if usage.Reported() {
		u.attempt = usage
	}
}

func (u *responseRetryUsage) attemptUsage() *modelcall.TokenUsage {
	copied := u.attempt
	return &copied
}

func (u *responseRetryUsage) total() modelcall.TokenUsage {
	a, b := u.completed, u.current
	return modelcall.TokenUsage{
		Present:                    a.Reported() || b.Reported(),
		Incomplete:                 a.Incomplete || b.Incomplete,
		PromptTokens:               a.PromptTokens + b.PromptTokens,
		CompletionTokens:           a.CompletionTokens + b.CompletionTokens,
		CacheReadInputTokens:       a.CacheReadInputTokens + b.CacheReadInputTokens,
		CacheCreationInputTokens:   a.CacheCreationInputTokens + b.CacheCreationInputTokens,
		CacheCreation1HInputTokens: a.CacheCreation1HInputTokens + b.CacheCreation1HInputTokens,
	}
}

func (u *responseRetryUsage) finishAttempt() {
	u.completed = u.total()
	if !u.current.Reported() {
		u.completed.Incomplete = true
	}
	u.current = modelcall.TokenUsage{}
}
