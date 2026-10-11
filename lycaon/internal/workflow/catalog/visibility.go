package catalog

import (
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// FilterProductCatalogSummaries keeps only catalog-visible bundled entries.
func FilterProductCatalogSummaries(rows []api.WorkflowSummary, manifests map[string]workflowdef.Manifest) []api.WorkflowSummary {
	if len(rows) == 0 {
		return rows
	}
	out := make([]api.WorkflowSummary, 0, len(rows))
	for _, row := range rows {
		if manifest, ok := manifests[workflowdef.ManifestKey(row.ID, row.Version)]; ok && manifest.Retired {
			continue
		}
		if row.Scope != api.WorkflowScopeBundled {
			out = append(out, row)
			continue
		}
		if m, ok := manifests[workflowdef.ManifestKey(row.ID, row.Version)]; ok && m.IsCatalogVisible() {
			out = append(out, row)
		}
	}
	return out
}
