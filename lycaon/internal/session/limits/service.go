package limits

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
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
	mu       sync.RWMutex
	defaults settings.SessionLimits
	provider Provider
	roots    Roots
	policy   ModelPolicySource
	windows  llm.ContextLengthLookup
	fallback ConfigSource
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
	var policy llm.ModelPolicy
	if policySource != nil {
		var err error
		policy, err = policySource.GetForProjectRoots(projectDirs)
		if err != nil {
			return fallbackBudget(fallbackConfig)
		}
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
	return cfg, llm.ScaleLimitsFromTrueWindow(modelinfo.DefaultFallbackTrueWindow)
}
