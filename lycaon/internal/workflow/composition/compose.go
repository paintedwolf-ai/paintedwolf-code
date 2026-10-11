package composition

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	// SessionComposeMaxPhases caps phases on session-tier compose manifests.
	SessionComposeMaxPhases = 4
)

// ComposeValidationFailed carries structured 422 errors from compose validation.
type ComposeValidationFailed struct {
	Errors []api.ComposeValidationError
}

func (e *ComposeValidationFailed) Error() string {
	if e == nil || len(e.Errors) == 0 {
		return "compose validation failed"
	}
	return e.Errors[0].Message
}

// ComposeRequest is input to session workflow compose.
type ComposeRequest struct {
	SessionID      string
	ProjectDir     string
	ManifestYAML   []byte
	SessionPosture api.SessionPosture
	CreatedBy      workflowdrafts.Actor
	DryRun         bool
}

// ComposeResult is a successful compose outcome.
type ComposeResult struct {
	Summary          api.WorkflowSummary
	EffectiveYAML    string
	EffectiveSummary api.ComposeEffectiveSummary
}

// Composer validates and registers session-scoped workflow manifests.
type Composer struct {
	// ModuleRoot resolves rules paths in development checkouts.
	ModuleRoot   string
	SessionStore workflowdrafts.Store
	Registry     *conditions.ConditionRegistry
	Obligations  workflowvalidation.ObligationSpecs
	Agents       orchestration.AgentRegistry
	Policy       *ComposePolicy
	Templates    TemplateCatalog
}

// Compose validates manifest YAML and optionally upserts the session tier.
func (c *Composer) Compose(ctx context.Context, req ComposeRequest) (*ComposeResult, error) {
	if c == nil || c.SessionStore == nil {
		return nil, fmt.Errorf("compose not configured")
	}
	if c.Registry == nil {
		return nil, &ComposeValidationFailed{Errors: []api.ComposeValidationError{{
			Field: "manifest", Code: "registry_unconfigured", Message: "condition registry required",
		}}}
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return nil, &ComposeValidationFailed{Errors: []api.ComposeValidationError{{
			Field: "session_id", Code: "session_id_required", Message: "session_id required",
		}}}
	}
	errs := c.validateBody(req.ManifestYAML)
	if len(errs) > 0 {
		return nil, &ComposeValidationFailed{Errors: errs}
	}
	raw, err := workflowdef.ParseManifestYAML(req.ManifestYAML)
	if err != nil {
		return nil, &ComposeValidationFailed{Errors: []api.ComposeValidationError{{
			Field: "manifest", Code: "parse_error", Message: err.Error(),
		}}}
	}
	parentCatalog, err := c.parentCatalog(req.ProjectDir)
	if err != nil {
		return nil, err
	}
	extendsRef := strings.TrimSpace(raw.Extends)
	parentEff, parentErr := c.resolveParentForSummary(extendsRef, parentCatalog)
	effective, err := workflowdef.ResolveManifestChain(raw, parentCatalog)
	if err != nil {
		return nil, &ComposeValidationFailed{Errors: workflowvalidation.ExtendsChainErrors(err)}
	}
	effective = workflowdef.FinalizeManifest(effective)
	maxPhases := SessionComposeMaxPhases
	if c.Policy != nil {
		maxPhases = c.Policy.MaxPhasesCap()
	}
	if len(effective.Phases) > maxPhases {
		return nil, &ComposeValidationFailed{Errors: []api.ComposeValidationError{{
			Field:   "phases",
			Code:    "max_phases_exceeded",
			Message: fmt.Sprintf("session compose allows at most %d phases; got %d", maxPhases, len(effective.Phases)),
		}}}
	}
	errs = append(errs, c.validateAgents(effective.AllowedAgents)...)
	errs = append(errs, c.validateRulesPaths(req.ProjectDir, effective.Rules)...)
	errs = append(errs, workflowvalidation.ValidateComposeManifest(c.Registry, c.Obligations, effective)...)
	if c.Policy != nil {
		errs = append(errs, c.Policy.Apply(ComposePolicyInput{
			Raw:            raw,
			Effective:      effective,
			ExtendsRef:     extendsRef,
			SessionPosture: req.SessionPosture,
		})...)
	}
	if len(errs) > 0 {
		return nil, &ComposeValidationFailed{Errors: errs}
	}
	effectiveYAML, err := workflowdef.MarshalManifestYAML(effective)
	if err != nil {
		return nil, err
	}
	summary := effective.Summary()
	summary.Scope = api.WorkflowScopeSession
	// Compute isolation from resolved topology bytes.
	catalog, err := extpacks.CatalogForConsumers()
	if err != nil {
		return nil, err
	}
	effSummary := buildEffectiveSummary(extendsRef, parentEff, parentErr, effective, catalog)
	if !req.DryRun {
		if !workflowdrafts.IsActor(req.CreatedBy) {
			return nil, fmt.Errorf("invalid compose actor %q", req.CreatedBy)
		}
		if err := c.SessionStore.Upsert(ctx, sessionID, []byte(effectiveYAML), req.CreatedBy, &effSummary); err != nil {
			return nil, err
		}
	}
	return &ComposeResult{
		Summary:          summary,
		EffectiveYAML:    effectiveYAML,
		EffectiveSummary: effSummary,
	}, nil
}

func (c *Composer) validateBody(data []byte) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	if len(strings.TrimSpace(string(data))) == 0 {
		out = append(out, api.ComposeValidationError{
			Field: "manifest", Code: "manifest_required", Message: "manifest body required",
		})
		return out
	}
	raw, err := workflowdef.ParseManifestYAML(data)
	if err != nil {
		out = append(out, api.ComposeValidationError{
			Field: "manifest", Code: "parse_error", Message: err.Error(),
		})
		return out
	}
	if strings.TrimSpace(raw.ID) == "" {
		out = append(out, api.ComposeValidationError{
			Field: "id", Code: "id_required", Message: "id required",
		})
	}
	if strings.TrimSpace(raw.Version) == "" {
		out = append(out, api.ComposeValidationError{
			Field: "version", Code: "version_required", Message: "version required",
		})
	}
	if strings.TrimSpace(raw.Trigger) != "" {
		out = append(out, api.ComposeValidationError{
			Field:   "trigger",
			Code:    "session_workflows_no_trigger",
			Message: "trigger is forbidden on session compose manifests",
		})
	}
	return out
}

func (c *Composer) parentCatalog(projectDir string) (map[string]workflowdef.Manifest, error) {
	reg, err := workflowdef.RegistryFromDirs(projectDir)
	if err != nil {
		return nil, err
	}
	return reg.All(), nil
}

func (c *Composer) resolveParentForSummary(extendsRef string, catalog map[string]workflowdef.Manifest) (workflowdef.Manifest, error) {
	extendsRef = strings.TrimSpace(extendsRef)
	if extendsRef == "" {
		return workflowdef.Manifest{}, nil
	}
	parent, ok := catalog[extendsRef]
	if !ok {
		return workflowdef.Manifest{}, &workflowdef.ExtendsError{Kind: workflowdef.ExtendsErrorUnknown, Ref: extendsRef}
	}
	return workflowdef.ResolveManifestChain(parent, catalog)
}

func (c *Composer) validateAgents(allowed []string) []api.ComposeValidationError {
	if c == nil {
		return workflowvalidation.ValidateAllowedAgents(nil, allowed)
	}
	return workflowvalidation.ValidateAllowedAgents(c.Agents, allowed)
}

func (c *Composer) validateRulesPaths(projectDir string, rules []string) []api.ComposeValidationError {
	moduleRoot := ""
	if c != nil && configlayout.IsModuleRoot(c.ModuleRoot) {
		moduleRoot = c.ModuleRoot
	}
	return workflowvalidation.ValidateRulesPaths(moduleRoot, projectDir, rules)
}

func buildEffectiveSummary(extendsRef string, parent workflowdef.Manifest, parentErr error, effective workflowdef.Manifest, catalog *extpacks.EffectiveCatalog) api.ComposeEffectiveSummary {
	summary := api.ComposeEffectiveSummary{
		Extends:      extendsRef,
		Phases:       []api.ComposePhaseSummary{},
		Feedback:     []api.ComposeFeedbackPhase{},
		Decisions:    []api.ComposeDecisionPhase{},
		GatesByPhase: map[string][]string{},
	}
	for _, p := range effective.PhaseDefs {
		phase := api.ComposePhaseSummary{
			ID:           p.ID,
			CompleteWhen: p.CompleteWhen,
			Next:         p.Next,
		}
		if p.OnEnter.SetPosture != "" {
			phase.OnEnter = &api.ComposePhaseOnEnter{SetPosture: p.OnEnter.SetPosture}
			summary.PostureTransitions = append(summary.PostureTransitions, api.ComposePostureTransition{
				Phase: p.ID, Posture: p.OnEnter.SetPosture,
			})
		}
		if mode := workflowdef.ForceExecutionMode(p.OnEnter.SetExecutionMode); mode != "" {
			if phase.OnEnter == nil {
				phase.OnEnter = &api.ComposePhaseOnEnter{}
			}
			phase.OnEnter.SetExecutionMode = mode
			summary.ExecutionModeTransitions = append(summary.ExecutionModeTransitions, api.ComposeExecutionModeTransition{
				Phase: p.ID, Mode: mode,
			})
		}
		if fb := p.OnEnter.RequestUserFeedback; fb != nil {
			if fb.ResolvedResponseType().IsChoice() {
				summary.Decisions = append(summary.Decisions, api.ComposeDecisionPhase{
					ID: p.ID, Prompt: fb.Prompt, Options: append([]string(nil), fb.Options...),
				})
			} else {
				summary.Feedback = append(summary.Feedback, api.ComposeFeedbackPhase{
					ID: p.ID, Prompt: fb.Prompt,
				})
			}
		}
		if len(p.Gates) > 0 {
			summary.GatesByPhase[p.ID] = append([]string(nil), p.Gates...)
			phase.Gates = append([]string(nil), p.Gates...)
		}
		summary.Phases = append(summary.Phases, phase)
	}
	if mode := workflowdef.ForceExecutionMode(effective.Controls.DefaultExecutionMode); mode != "" {
		summary.DefaultExecutionMode = mode
	} else if workflowdef.NormalizeExecutionMode(effective.Controls.DefaultExecutionMode) == workflowdef.ExecutionModeStateDerived {
		summary.DefaultExecutionMode = workflowdef.ExecutionModeStateDerived
	}
	if parentErr == nil && parent.ID != "" {
		parentIDs := map[string]struct{}{}
		for _, id := range parent.Phases {
			parentIDs[id] = struct{}{}
		}
		effectiveIDs := map[string]struct{}{}
		for _, id := range effective.Phases {
			effectiveIDs[id] = struct{}{}
		}
		for id := range parentIDs {
			if _, ok := effectiveIDs[id]; !ok {
				summary.PhasesRemovedFromParent = append(summary.PhasesRemovedFromParent, id)
			}
		}
	}
	summary.CoordinatorBrief = composeCoordinatorBrief(summary, effective)
	summary.RequiresIsolation = manifestRequiresIsolation(effective, catalog)
	return summary
}

func composeCoordinatorBrief(s api.ComposeEffectiveSummary, effective workflowdef.Manifest) string {
	var parts []string
	if s.Extends != "" {
		parts = append(parts, fmt.Sprintf("Extends %s.", s.Extends))
	}
	if len(s.PhasesRemovedFromParent) > 0 {
		parts = append(parts, fmt.Sprintf("Skips parent phases: %s.", strings.Join(s.PhasesRemovedFromParent, ", ")))
	}
	if len(s.PostureTransitions) > 0 {
		var pts []string
		for _, pt := range s.PostureTransitions {
			pts = append(pts, fmt.Sprintf("%s→%s", pt.Phase, pt.Posture))
		}
		parts = append(parts, fmt.Sprintf("Posture transitions: %s.", strings.Join(pts, ", ")))
	}
	if dm := strings.TrimSpace(s.DefaultExecutionMode); dm != "" {
		parts = append(parts, fmt.Sprintf("Default execution mode: %s.", dm))
	}
	if len(s.ExecutionModeTransitions) > 0 {
		var ets []string
		for _, et := range s.ExecutionModeTransitions {
			ets = append(ets, fmt.Sprintf("%s→%s", et.Phase, et.Mode))
		}
		parts = append(parts, fmt.Sprintf("Execution mode transitions: %s.", strings.Join(ets, ", ")))
	}
	if len(s.GatesByPhase) > 0 {
		parts = append(parts, "Closeout gates apply on implement (or as listed per phase).")
	}
	if len(s.Feedback) > 0 {
		parts = append(parts, "Includes feedback phases requiring user chat response.")
	}
	if len(s.Decisions) > 0 {
		parts = append(parts, "Includes decision phases requiring explicit user choice (chat is not consent).")
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Session workflow %s@%s with phases %s.", effective.ID, effective.Version, strings.Join(effective.Phases, " → "))
	}
	return strings.Join(parts, " ")
}
