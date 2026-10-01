package workflowvalidate

import (
	"fmt"
	"sort"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// WorkflowSource ties a registry key to its authored path.
type WorkflowSource struct {
	Key    string
	Path   string
	Origin workflowdef.ManifestSourceOrigin
}

// CatalogSources resolves the registry AND its per-manifest source index from
// the same load, so discovery and validation can never diverge.
func CatalogSources(opts CatalogValidateOptions) (*workflowdef.Registry, []WorkflowSource, error) {
	var projectDir string
	switch opts.Mode {
	case ModeProject:
		projectDir = opts.ProjectDir
		if projectDir == "" {
			projectDir = "."
		}
	case ModePaths:
		// Paths mode still loads bundled base; overlay paths are validated separately.
		projectDir = opts.ProjectDir
	case ModeBundled:
		projectDir = ""
	default:
		return nil, nil, fmt.Errorf("unknown catalog validate mode %d", opts.Mode)
	}
	reg, srcMap, err := workflowdef.RegistryFromDirsWithSources(projectDir)
	if err != nil {
		return nil, nil, err
	}
	keys := make([]string, 0, len(srcMap))
	for k := range srcMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sources := make([]WorkflowSource, 0, len(keys))
	for _, k := range keys {
		s := srcMap[k]
		sources = append(sources, WorkflowSource{Key: s.Key, Path: s.Path, Origin: s.Origin})
	}
	return reg, sources, nil
}
