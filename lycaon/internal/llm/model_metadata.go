package llm

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"sync/atomic"
	"time"
)

// ModelCatalogUnavailableError means discovery could not establish an assignment.
type ModelCatalogUnavailableError struct {
	ProviderID string
	Model      string
	Cause      error
}

func (e *ModelCatalogUnavailableError) Error() string {
	return fmt.Sprintf("cannot validate model %q for provider %q: model discovery failed: %v", e.Model, e.ProviderID, e.Cause)
}

func (e *ModelCatalogUnavailableError) Unwrap() error { return e.Cause }

// Model-family knowledge cutoffs define training data recency for prompt assembly.

// CutoffRule maps model-id substrings to a knowledge-cutoff month.
type CutoffRule struct {
	// Match is a list of case-insensitive substrings tested against the model
	// id; any hit selects the rule.
	Match []string `yaml:"match"`
	// Cutoff is the approximate end of the family's training data, "YYYY-MM".
	Cutoff string `yaml:"cutoff"`
}

var modelCutoffRules atomic.Pointer[[]CutoffRule]

// SetModelCutoffRules installs the active rule set (user rules first, then
// bundled — first match wins). ProviderCatalog.Reload is the production caller.
func SetModelCutoffRules(rules []CutoffRule) {
	own := append([]CutoffRule(nil), rules...)
	modelCutoffRules.Store(&own)
}

func activeModelCutoffRules() []CutoffRule {
	if p := modelCutoffRules.Load(); p != nil {
		return *p
	}
	return nil
}

const cutoffMonthLayout = "2006-01"

// ValidateCutoffRules rejects rules with an empty match list or a cutoff that
// is not a YYYY-MM month — a bad rule set fails the catalog load rather than
// silently rendering junk into prompts.
func ValidateCutoffRules(rules []CutoffRule) error {
	for i, rule := range rules {
		if len(rule.Match) == 0 {
			return fmt.Errorf("model_cutoff[%d]: empty match list", i)
		}
		for _, m := range rule.Match {
			if strings.TrimSpace(m) == "" {
				return fmt.Errorf("model_cutoff[%d]: blank match pattern", i)
			}
		}
		if _, err := time.Parse(cutoffMonthLayout, strings.TrimSpace(rule.Cutoff)); err != nil {
			return fmt.Errorf("model_cutoff[%d]: cutoff %q is not YYYY-MM", i, rule.Cutoff)
		}
	}
	return nil
}

// ResolveModelCutoff returns the knowledge-cutoff month ("YYYY-MM") for a
// model id, or "" when no family rule matches.
func ResolveModelCutoff(model string) string {
	for _, rule := range activeModelCutoffRules() {
		if modelinfo.MatchFirstSubstring(model, rule.Match) {
			return strings.TrimSpace(rule.Cutoff)
		}
	}
	return ""
}

// resolveSelectedModel uses the current catalog for a known model. Unknown
// selections still require discovery before role and capability validation.
func (r *registryRouting) resolveSelectedModel(ctx context.Context, providerID, model string) mergeResult {
	result, refresh := r.modelSnapshot(ctx, providerID)
	kind, _ := r.ProviderKind(providerID)
	for _, entry := range result.Models {
		if modelinfo.EquivalentID(kind, entry.ID, model) {
			refresh()
			return result
		}
	}
	return r.resolveModels(ctx, providerID)
}

func (r *registryRouting) modelSnapshot(ctx context.Context, providerID string) (mergeResult, func()) {
	noop := func() {}
	snapshot := r.snapshot.Load()
	if snapshot == nil || !snapshot.configured[providerID] {
		return mergeResult{DiscoveryStatus: DiscoveryStatusSkipped}, noop
	}
	entry, ok := snapshot.entries[providerID]
	if !ok {
		return mergeResult{DiscoveryStatus: DiscoveryStatusSkipped}, noop
	}
	profile := providerprofile.Default()
	if provider := snapshot.providers[providerID]; provider != nil {
		profile = provider.Profile()
	}
	key := snapshot.credentialValues[providerID]
	cacheKey := discoveryCacheKey(providerID, profile, entry.BaseURL, key)
	var models []modelinfo.Entry
	var lastErr error
	fresh := false
	if r.discovery != nil {
		models, _, fresh, lastErr = r.discovery.Snapshot(cacheKey)
	}
	feed, status, usable := r.feedSnapshot()
	// A transient refresh failure does not erase the last successful catalog.
	result := mergeAssignableModels(entry.Kind, entry.Models, models, nil, feed, status, usable)
	if lastErr != nil {
		result.DiscoveryError = lastErr
		result.DiscoveryStatus = DiscoveryStatusError
	}
	r.publishHydratedProviders(snapshot, map[string][]modelinfo.Entry{providerID: result.Models})
	if fresh || r.discovery == nil || profile.Discovery == providerprofile.DiscoveryNone {
		return result, noop
	}
	return result, func() {
		r.discovery.Refresh(ctx, cacheKey, func(ctx context.Context) ([]modelinfo.Entry, error) {
			return fetchDiscoveredModels(ctx, r.discoveryClient, profile, entry.BaseURL, key)
		})
	}
}

// ModelRoleEligibility is shared by assignment, readiness and presentation.
func ModelRoleEligibility(kind string, model modelinfo.Entry, role string, exclusions RoleExclusions) api.ModelRoleEligibility {
	result := api.ModelRoleEligibility{State: "eligible", Selectable: true, Code: "compatible", Reason: "The required capabilities are supported."}
	if role != PolicySlotCoordinator && role != PolicySlotAgentPool && role != PolicySlotLite {
		return api.ModelRoleEligibility{State: "incompatible", Code: "unknown_role", Reason: "This model role is not supported."}
	}
	if rule := exclusions.Match(kind, model.ID, role); rule != nil {
		return api.ModelRoleEligibility{State: "incompatible", Code: "role_excluded", Reason: rule.Reason, RuleID: rule.ID}
	}
	caps := model.EffectiveCapabilities()
	requirements := []struct {
		name     string
		evidence modelinfo.CapabilityEvidence
	}{{"chat", caps.Chat}}
	if role != PolicySlotLite {
		requirements = append(requirements, struct {
			name     string
			evidence modelinfo.CapabilityEvidence
		}{"tools", caps.Tools})
	}
	for _, requirement := range requirements {
		if modelinfo.Refused(requirement.evidence) {
			return api.ModelRoleEligibility{State: "incompatible", Code: requirement.name + "_unsupported", Reason: "The provider reports that this model does not support " + requirement.name + "."}
		}
		if !modelinfo.Supported(requirement.evidence) && result.State == "eligible" {
			result = api.ModelRoleEligibility{State: "unverified", Selectable: result.Selectable && requirement.name == "tools", Code: requirement.name + "_unknown", Reason: "Support for " + requirement.name + " is not listed for this model."}
		}
	}
	return result
}

func modelRolesToAPI(kind string, model modelinfo.Entry, doc RoleExclusions) api.ModelRoleEligibilitySet {
	return api.ModelRoleEligibilitySet{
		Coordinator: ModelRoleEligibility(kind, model, PolicySlotCoordinator, doc),
		AgentPool:   ModelRoleEligibility(kind, model, PolicySlotAgentPool, doc),
		Lite:        ModelRoleEligibility(kind, model, PolicySlotLite, doc),
	}
}

func ReadyToAssignExistsSlot(configured bool, kind string, models []modelinfo.Entry, doc RoleExclusions) bool {
	if !configured {
		return false
	}
	for _, model := range models {
		if model.ID == "" {
			continue
		}
		for _, role := range []string{PolicySlotCoordinator, PolicySlotLite} {
			if ModelRoleEligibility(kind, model, role, doc).Selectable {
				return true
			}
		}
	}
	return false
}

// ContextLength returns the usable context window for provider/model from
// already-known ModelEntry data only (catalog YAML + discovery cache). It does
// not perform live HTTP discovery.
func (r *registryRouting) ContextLength(provider, model string) int {
	if r == nil {
		return 0
	}
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if model == "" {
		return 0
	}
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return 0
	}
	entry, ok := snapshot.entries[provider]
	if !ok {
		return 0
	}
	for _, m := range entry.Models {
		if modelinfo.EquivalentID(entry.Kind, m.ID, model) && m.ContextLength > 0 {
			return m.ContextLength
		}
	}
	profile := providerprofile.Default()
	if p := snapshot.providers[provider]; p != nil {
		profile = p.Profile()
	}
	cacheKey := discoveryCacheKey(provider, profile, entry.BaseURL, snapshot.credentialValues[provider])
	if cached, ok, _ := r.discovery.Get(cacheKey); ok {
		for _, m := range cached {
			if modelinfo.EquivalentID(entry.Kind, m.ID, model) && m.ContextLength > 0 {
				return m.ContextLength
			}
		}
	}
	return 0
}

func (r *Registry) RequestBudget(ctx context.Context, providerID string, req modelcall.CompletionRequest) modelcall.RequestBudget {
	req.Messages = transcript.Project(req.Messages)
	budget := modelcall.RequestBudget{Input: modelcall.EstimateRequestInput(req), Output: modelcall.CompletionTokenLimit(req, 0, modelcall.OrchestrationMaxTokens, req.Think, false), Headroom: 1024}
	if r == nil {
		return budget
	}
	// Planning reads the adapter without dispatch middleware.
	if snapshot := r.snapshot.Load(); snapshot != nil {
		provider := snapshot.providers[providerID]
		if provider != nil {
			policy := thinkingPolicyProvider{Provider: provider, registry: r}
			if prepared, err := policy.prepare(ctx, req, nil); err == nil {
				req = prepared
			}
		}
		if reporter, ok := provider.(modelcall.RequestBudgetReporter); ok {
			budget = reporter.RequestBudget(ctx, req)
		}
	}
	budget.MinimumOutput = max(budget.MinimumOutput, modelcall.MinimumCompletionOutput(req))
	if limit := r.ContextLength(providerID, req.Model); limit > 0 && (budget.Window <= 0 || limit < budget.Window) {
		budget.Window = limit
	}
	return budget
}
