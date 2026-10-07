package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

const retiredDefinitionsPin = "lycaon/test/contract/testdata/workflows/retired-definitions.yaml"

// A retired definition is the live contract of every run that selected its
// version, so its bytes stay what the release shipped. New behaviour is a new
// version beside it, never an edit to the retired file.
func TestRetiredWorkflowDefinitionsAreFrozen(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), retiredDefinitionsPin))
	contractcheck.FailErr(t, "read retired definition pins", err)
	pinned := map[string]string{}
	contractcheck.FailErr(t, "decode retired definition pins", yaml.Unmarshal(raw, &pinned))

	catalog, err := extpacks.CatalogForConsumers()
	contractcheck.FailErr(t, "resolve bundled catalog", err)
	seen := map[string]bool{}
	for _, unitID := range catalog.LoadedUnitIDs() {
		id, ok := strings.CutPrefix(unitID, extpacks.WorkflowUnitIDPrefix)
		if !ok || id == "" || strings.HasPrefix(id, "_") || strings.Contains(id, "/") {
			continue
		}
		data, _, ok := catalog.UnitContent(unitID)
		if !ok {
			continue
		}
		manifest, err := workflowdef.ParseManifestYAML(data)
		contractcheck.FailErr(t, "parse "+unitID, err)
		if !manifest.Retired {
			continue
		}
		key := workflowdef.ManifestKey(manifest.ID, manifest.Version)
		seen[key] = true
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		want, pinnedHere := pinned[key]
		switch {
		case !pinnedHere:
			t.Errorf("%s is retired but has no pin in %s; add its digest %s when retiring it", key, retiredDefinitionsPin, digest)
		case want != digest:
			t.Errorf("%s changed after retirement (digest %s, pinned %s); retired definitions are frozen, so ship the change as a new version", key, digest, want)
		}
	}
	for key := range pinned {
		if !seen[key] {
			t.Errorf("%s is pinned in %s but no bundled retired definition carries that id and version", key, retiredDefinitionsPin)
		}
	}
}
