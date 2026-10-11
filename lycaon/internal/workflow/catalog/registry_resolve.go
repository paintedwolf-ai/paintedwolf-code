package catalog

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
)

// Resolver merges the pack, project, and session workflow tiers.
type RunReader interface {
	Get(context.Context, string) (*api.WorkflowRun, error)
}

type Resolver struct {
	Runs               RunReader
	Overlay            *workflowdef.Registry
	Sessions           SessionReader
	ProjectDirFallback func(context.Context, string) (string, error)
	SessionStore       workflowdrafts.Store
	// CatalogFor resolves the request-scoped catalog.
	CatalogFor func(ctx context.Context, projectDir, sessionID string) *extpacks.EffectiveCatalog
	// ProjectTierApplies reports whether {projectDir}/<overlay>/workflows may merge.
	// Nil disables project workflow overlays.
	ProjectTierApplies func(ctx context.Context, projectDir string) bool
}

// Resolve returns an effective registry and per-entry scope annotations.
func (r Resolver) Resolve(ctx context.Context, projectDir, sessionID string) (*workflowdef.Registry, map[string]string, error) {
	reg, scopes, _, err := r.ResolveWithExcluded(ctx, projectDir, sessionID)
	return reg, scopes, err
}

// ResolveWithExcluded includes scope annotations and diagnostics for excluded manifests.
func (r Resolver) ResolveWithExcluded(ctx context.Context, projectDir, sessionID string) (*workflowdef.Registry, map[string]string, []api.ExcludedWorkflow, error) {
	resolved, scopes, excluded, err := mergeManifestTiers(ctx, projectDir, sessionID, r.SessionStore, func(c context.Context, dir, sid string) *extpacks.EffectiveCatalog {
		if r.CatalogFor == nil {
			return nil
		}
		return r.CatalogFor(c, dir, sid)
	}, func(c context.Context, dir string) bool {
		if r.ProjectTierApplies == nil {
			return false
		}
		return r.ProjectTierApplies(c, dir)
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return workflowdef.NewRegistry(resolved), scopes, excluded, nil
}

// ListResolved returns catalog summaries with scope for each manifest entry.
func (r Resolver) ListResolved(ctx context.Context, projectDir, sessionID string) ([]api.WorkflowSummary, error) {
	summaries, _, err := r.ListResolvedWithExcluded(ctx, projectDir, sessionID)
	return summaries, err
}

// ListResolvedWithExcluded returns catalog summaries and excluded manifests with diagnostics.
func (r Resolver) ListResolvedWithExcluded(ctx context.Context, projectDir, sessionID string) ([]api.WorkflowSummary, []api.ExcludedWorkflow, error) {
	reg, scopes, excluded, err := r.ResolveWithExcluded(ctx, projectDir, sessionID)
	if err != nil {
		return nil, nil, err
	}
	return FilterProductCatalogSummaries(reg.SummariesWithScopes(scopes), reg.All()), excluded, nil
}

func mergeManifestTiers(ctx context.Context, projectDir, sessionID string, sessionStore workflowdrafts.Store, catalogFor func(context.Context, string, string) *extpacks.EffectiveCatalog, projectTierApplies func(context.Context, string) bool) (map[string]workflowdef.Manifest, map[string]string, []api.ExcludedWorkflow, error) {
	var catalog *extpacks.EffectiveCatalog
	if catalogFor != nil {
		catalog = catalogFor(ctx, projectDir, sessionID)
	}
	bundled, _, err := workflowdef.LoadPackManifestsForCatalog(catalog)
	if err != nil {
		return nil, nil, nil, err
	}
	if _, err := workflowdef.ResolveAllManifests(bundled); err != nil {
		return nil, nil, nil, err
	}
	candidates := manifestCandidates{}
	for _, m := range bundled {
		candidates.add(manifestCandidate{manifest: m, scope: string(api.WorkflowScopeBundled)})
	}
	var excluded []api.ExcludedWorkflow
	if strings.TrimSpace(projectDir) != "" && projectTierApplies != nil && projectTierApplies(ctx, projectDir) {
		if err := collectProjectManifests(projectDir, candidates, &excluded); err != nil {
			return nil, nil, nil, err
		}
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID != "" && sessionStore != nil {
		rows, err := sessionStore.ListBySession(ctx, sessionID)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, row := range rows {
			path := "session:" + workflowdef.ManifestKey(row.WorkflowID, row.Version)
			candidates.parse([]byte(row.ManifestYAML), path, string(api.WorkflowScopeSession), &excluded)
		}
	}
	resolved, scopes, rejected := candidates.resolve()
	excluded = append(excluded, rejected...)
	return resolved, scopes, excluded, nil
}

func validateUntrustedManifest(path string, m workflowdef.Manifest) []api.ComposeValidationError {
	if strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.Version) == "" {
		return []api.ComposeValidationError{
			workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"), path,
				map[string]any{"detail": "workflow id and version required"}),
		}
	}
	if m.Attach.Policy == workflowdef.AttachPolicySessionCreate {
		field := "attach.policy"
		if path != "" {
			field = path + ": attach.policy"
		}
		return []api.ComposeValidationError{
			workflowdiag.EmitDefault(workflowdiag.MustCode("attach_session_create_on_overlay"), field,
				map[string]any{"policy": string(m.Attach.Policy)}),
		}
	}
	return nil
}

func validateResolvedManifest(path string, eff workflowdef.Manifest) []api.ComposeValidationError {
	var diags []api.ComposeValidationError

	if eff.Request == nil {
		field := "request"
		if path != "" {
			field = path + ": request"
		}
		diags = append(diags, workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"), field,
			map[string]any{"detail": "request required"}))
	}
	if err := workflowdef.ValidatePhaseTargets(eff); err != nil {
		field := "phases"
		if path != "" {
			field = path + ": phases"
		}
		diags = append(diags, workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"), field,
			map[string]any{"detail": err.Error()}))
	}
	return diags
}

func newExcludedWorkflow(id, version, path string, errs []api.ComposeValidationError) api.ExcludedWorkflow {
	return api.ExcludedWorkflow{
		ID:      id,
		Version: version,
		Path:    path,
		Errors:  errs,
	}
}

func extendsChainErrorWithPrefix(path string, err error) []api.ComposeValidationError {
	diags := workflowvalidation.ExtendsChainErrors(err)
	if path != "" {
		for i := range diags {
			if diags[i].Field != "" {
				diags[i].Field = path + ": " + diags[i].Field
			} else {
				diags[i].Field = path
			}
		}
	}
	return diags
}

func (m *Resolver) ForRunID(ctx context.Context, runID string) (workflowdef.Manifest, error) {
	run, err := m.Runs.Get(ctx, runID)
	if err != nil {
		return workflowdef.Manifest{}, err
	}
	if run == nil {
		return workflowdef.Manifest{}, nil
	}
	return m.ForRun(ctx, run)
}
