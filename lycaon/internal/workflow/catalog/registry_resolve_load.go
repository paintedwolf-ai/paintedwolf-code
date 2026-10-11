package catalog

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
)

func collectProjectManifests(projectDir string, candidates manifestCandidates, excluded *[]api.ExcludedWorkflow) error {
	dir := filepath.Join(projectDir, settingsoverlay.DirName(), "workflows")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), "_") {
			continue
		}
		path := filepath.Join(settingsoverlay.DirName(), "workflows", entry.Name(), "workflow.yaml")
		data, err := os.ReadFile(filepath.Join(projectDir, path))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			*excluded = append(*excluded, manifestLoadFailure(path, err))
			continue
		}
		candidates.parse(data, path, string(api.WorkflowScopeProject), excluded)
	}
	return nil
}

func manifestLoadFailure(path string, err error) api.ExcludedWorkflow {
	return newExcludedWorkflow("", "", path, []api.ComposeValidationError{
		workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"), path, map[string]any{"detail": err.Error()}),
	})
}

func (c manifestCandidates) parse(data []byte, path, scope string, excluded *[]api.ExcludedWorkflow) {
	m, err := workflowdef.ParseManifestYAML(data)
	if err != nil {
		*excluded = append(*excluded, manifestLoadFailure(path, err))
		return
	}
	if diags := validateUntrustedManifest(path, m); len(diags) > 0 {
		*excluded = append(*excluded, newExcludedWorkflow(m.ID, m.Version, path, diags))
		return
	}
	c.add(manifestCandidate{manifest: m, path: path, scope: scope})
}
