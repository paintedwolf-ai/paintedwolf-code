package limits

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

type Provider interface {
	SessionLimits(string) settings.SessionLimits
}
type Roots interface {
	SettingsPath(context.Context, *api.Session) string
	SettingsRoots(context.Context, *api.Session) []string
}
type ModelPolicySource interface {
	GetForProjectRoots([]string) (llm.ModelPolicy, error)
}
type ConfigSource interface {
	Config() compaction.CompactionConfig
}

type Service struct {
	mu                 sync.RWMutex
	defaults           settings.SessionLimits
	provider           Provider
	roots              Roots
	policy             ModelPolicySource
	windows            llm.ContextLengthLookup
	fallback           ConfigSource
	sessionLiveWindows scopedstore.LRU[int]
}

func New(defaults settings.SessionLimits, roots Roots) *Service {
	return &Service{defaults: settings.NormalizeSessionLimits(defaults), roots: roots}
}
func (m *Service) Defaults() settings.SessionLimits {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.defaults
}
func (m *Service) SetDefaults(defaults settings.SessionLimits) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaults = settings.NormalizeSessionLimits(defaults)
}
func (m *Service) SetProvider(provider Provider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.provider = provider
}
func (m *Service) SetModelSources(policy ModelPolicySource, windows llm.ContextLengthLookup) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policy, m.windows = policy, windows
}
func (m *Service) SetFallback(config ConfigSource) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fallback = config
}
func (m *Service) SetMaxIterations(n int) {
	if n > 0 {
		m.mu.Lock()
		m.defaults.MaxIterations = n
		m.mu.Unlock()
	}
}

// LowerSessionLiveWindow caps a session's live context window to window tokens
// after a provider reports a smaller context ceiling at runtime.
func (m *Service) LowerSessionLiveWindow(sessionID string, window int) {
	if m == nil || sessionID == "" || window <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.sessionLiveWindows.Load(sessionID); !ok || window < cur {
		m.sessionLiveWindows.Store(sessionID, window)
	}
}

// SessionLiveWindow returns any runtime-lowered live context window for sessionID.
func (m *Service) SessionLiveWindow(sessionID string) (int, bool) {
	if m == nil || sessionID == "" {
		return 0, false
	}
	w, ok := m.sessionLiveWindows.Load(sessionID)
	return w, ok && w > 0
}

func applySessionLiveWindow(cfg compaction.CompactionConfig, liveWindow int) compaction.CompactionConfig {
	if liveWindow <= 0 {
		return cfg
	}
	if cfg.ModelContextWindow > 0 && cfg.ModelContextWindow <= liveWindow {
		return cfg
	}
	cfg.ModelContextWindow = liveWindow
	cfg.HardCeilingTokens = 0
	cfg.TargetTokens = 0
	if normalized, err := compaction.NormalizeConfig(cfg); err == nil {
		return normalized
	}
	return cfg
}

func (m *Service) Effective(ctx context.Context, sess *api.Session) settings.SessionLimits {
	m.mu.RLock()
	lim, provider, roots := m.defaults, m.provider, m.roots
	m.mu.RUnlock()
	projectDir := ""
	var rootPaths []string
	if roots != nil {
		projectDir = roots.SettingsPath(ctx, sess)
		rootPaths = roots.SettingsRoots(ctx, sess)
	}
	if provider != nil && sess != nil {
		lim = provider.SessionLimits(projectDir)
	}
	_, derived := m.ResolveRoots(rootPaths)
	return ApplyWorkerMaxToolLoops(settings.ApplyDerivedSessionLimits(lim, derived), sess)
}

func (m *Service) Compaction(ctx context.Context, sess *api.Session) compaction.CompactionConfig {
	var paths []string
	if m.roots != nil {
		paths = m.roots.SettingsRoots(ctx, sess)
	}
	cfg, _ := m.ResolveRoots(paths)
	if sess == nil {
		return cfg
	}
	if sess.Model != "" {
		m.mu.RLock()
		lookup := m.windows
		m.mu.RUnlock()
		policy := llm.ModelPolicy{Coordinator: llm.ModelRef{ProviderID: sess.ProviderID, Model: sess.Model}}
		if resolved, _, err := llm.ApplyLiveBudget(cfg, policy, lookup, modelinfo.DefaultModelContextWindows()); err == nil {
			cfg = resolved
		}
	}
	if liveWindow, ok := m.SessionLiveWindow(sess.ID); ok {
		cfg = applySessionLiveWindow(cfg, liveWindow)
	}
	return cfg
}

func (m *Service) ResolveRoots(projectDirs []string) (compaction.CompactionConfig, llm.SessionLimitFields) {
	var policySource ModelPolicySource
	var lookup llm.ContextLengthLookup
	var fallbackConfig ConfigSource
	if m != nil {
		m.mu.RLock()
		policySource, lookup, fallbackConfig = m.policy, m.windows, m.fallback
		m.mu.RUnlock()
	}
	if policySource == nil {
		return fallbackBudget(fallbackConfig)
	}
	policy, err := policySource.GetForProjectRoots(projectDirs)
	if err != nil {
		return fallbackBudget(fallbackConfig)
	}
	cfg, derived, err := llm.ApplyLiveBudget(compaction.DefaultCompactionConfig(), policy, lookup, modelinfo.DefaultModelContextWindows())
	if err != nil {
		return fallbackBudget(fallbackConfig)
	}
	return cfg, derived
}

func fallbackBudget(source ConfigSource) (compaction.CompactionConfig, llm.SessionLimitFields) {
	cfg := compaction.DefaultCompactionConfig()
	if source != nil {
		cfg = source.Config()
	}
	derived := llm.ScaleLimitsFromTrueWindow(modelinfo.DefaultFallbackTrueWindow)
	if cfg.MaxWorkerSummaryChars <= 0 {
		cfg.MaxWorkerSummaryChars = derived.MaxWorkerSummaryChars
	}
	return cfg, derived
}
