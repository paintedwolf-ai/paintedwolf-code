package llm

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

// NewEmptyRegistry constructs an unseeded registry for explicit provider registration.
func NewEmptyRegistry() *Registry {
	reg := &Registry{}
	reg.snapshot.Store(newProviderRegistrySnapshot(providerRegistrySnapshotInput{}))
	reg.SetOutboundSecretScreen(NewModelSecretScreen(nil, nil))
	return reg
}

// NewUtilityPlaneWithOverlayWait constructs a shared utility plane with an explicit overlay wait bound.
func NewUtilityPlaneWithOverlayWait(overlayWait time.Duration) *UtilityPlane {
	lanes := newUtilityLanes()
	lanes.overlayWait = overlayWait
	return &UtilityPlane{
		health: newSlotHealth(),
		lanes:  lanes,
	}
}

// AcquireForeground claims the provider's foreground utility lane.
func (p *UtilityPlane) AcquireForeground(ctx context.Context, provider modelcall.Provider) (func(), error) {
	return p.acquireForeground(ctx, provider)
}

// SummaryRevision identifies the routed utility policy that plans summaries.
func (r *RegistrySummarizer) SummaryRevision(ctx context.Context) (string, error) {
	if r.Policy == nil {
		return "local", nil
	}
	policy, err := r.modelPolicy(ctx)
	if err != nil {
		return "", err
	}
	return compaction.RevisionDigest(struct {
		Policy  ModelPolicy
		Class   UtilityClass
		Purpose string
	}{policy, r.utilityClass(), r.Purpose})
}

// SummaryBudget reserves utility output and framing inside the selected window.
func (r *RegistrySummarizer) SummaryBudget(ctx context.Context) compaction.SummaryBudget {
	provider, model, err := r.liteProvider(ctx)
	if err != nil || provider == nil {
		return compaction.SummaryBudget{}
	}
	window := r.Registry.ContextLength(provider.ID(), model)
	if window <= 0 {
		return compaction.SummaryBudget{}
	}
	output := min(1536, max(1, window/8))
	return compaction.SummaryBudget{Input: max(1, window*3/4-output-summaryRequestHeadroom), Output: output}
}

// Explicit bindings take precedence over the invoking session's settings roots.
func (r *RegistrySummarizer) modelPolicy(ctx context.Context) (ModelPolicy, error) {
	if r.Scope != "" || r.ProjectDir != "" {
		scope := r.Scope
		if scope == "" {
			scope = SettingsScopeProject
		}
		return r.Policy.Get(scope, r.ProjectDir)
	}
	return r.Policy.GetForProjectRoots(curationctx.SessionFrom(ctx).SettingsRoots)
}
