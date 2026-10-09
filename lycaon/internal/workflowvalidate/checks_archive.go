package workflowvalidate

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
)

// checkArchivedGuidance requires every inject a sealed version renders to be
// sealed with it, so a resumed run never reads a later version's guidance.
func checkArchivedGuidance(path string, m workflowdef.Manifest) []api.ComposeValidationError {
	catalog, err := extpacks.CatalogForConsumers()
	if err != nil {
		return []api.ComposeValidationError{workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"), path,
			map[string]any{"detail": err.Error()})}
	}
	key := extpacks.ArchiveKey(m.ID, m.Version)
	var out []api.ComposeValidationError
	for _, inject := range m.Injects {
		render := strings.TrimSpace(inject.Render)
		if render == "" {
			continue
		}
		if _, _, ok := catalog.UnitContent(extpacks.ArchiveGuidanceUnitID(key, render)); !ok {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"), path,
				map[string]any{"detail": fmt.Sprintf("%s: sealed workflow %s does not seal guidance %q", path, key, render)}))
		}
	}
	return out
}
