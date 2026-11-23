package catalog

import (
	"github.com/lycaon/lycaon/internal/extpacks"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func loadProjectCandidates(catalog *extpacks.EffectiveCatalog, projectDir string, includeProject bool, replacingPath string) (manifestCandidates, []api.ExcludedWorkflow, error) {
	bundled, _, err := workflowdef.LoadPackManifestsForCatalog(catalog)
	if err != nil {
		return nil, nil, err
	}
	if _, err := workflowdef.ResolveAllManifests(bundled); err != nil {
		return nil, nil, err
	}
	candidates := manifestCandidates{}
	for _, m := range bundled {
		candidates.add(manifestCandidate{manifest: m, scope: string(api.WorkflowScopeBundled)})
	}
	var excluded []api.ExcludedWorkflow
	if includeProject {
		if err := collectProjectManifests(projectDir, replacingPath, candidates, &excluded); err != nil {
			return nil, nil, err
		}
	}
	return candidates, excluded, nil
}

// ValidatePublication resolves the proposed bytes in their destination tier.
func ValidatePublication(projectDir, path string, data []byte) ([]api.ComposeValidationError, error) {
	candidates, excluded, err := loadProjectCandidates(nil, projectDir, true, path)
	if err != nil {
		return nil, err
	}
	candidates.parse(data, path, string(api.WorkflowScopeProject), &excluded)
	_, _, rejected := candidates.resolve()
	for _, item := range append(excluded, rejected...) {
		if item.Path == path {
			return item.Errors, nil
		}
	}
	return nil, nil
}
