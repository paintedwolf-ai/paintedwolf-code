package workflowfixture

import (
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// LoadMergedWorkflowCatalog resolves the shipped workflow catalog through the
// effective catalog — the only place a manifest's bytes come from.
func LoadMergedWorkflowCatalog(t *testing.T) (map[string]workflowdef.Manifest, error) {
	t.Helper()
	reg, err := workflowdef.RegistryFromDirs("")
	if err != nil {
		return nil, err
	}
	return reg.All(), nil
}
