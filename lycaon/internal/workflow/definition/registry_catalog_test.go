package definition

import (
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
)

func TestRegistryReDerivesWhenTheCatalogMoves(t *testing.T) {
	reg := &Registry{
		entries:  cloneManifests(map[string]Manifest{}),
		revision: "generation-1",
	}
	derived := 0
	reg.derive = func() (map[string]Manifest, error) {
		derived++
		return map[string]Manifest{
			ManifestKey("acme-flow", "1.0.0"): {ID: "acme-flow", Version: "1.0.0"},
		}, nil
	}

	if _, err := reg.Get("acme-flow", "1.0.0"); err != nil {
		t.Fatalf("workflow should be startable after re-derivation: %v", err)
	}
	if derived != 1 {
		t.Fatalf("derive calls = %d, want 1", derived)
	}
	if reg.revision != activeCatalogRevision() {
		t.Fatalf("registry revision = %q, want the active catalog's", reg.revision)
	}
}

func TestRegistryKeepsTheLastGoodSetWhenDeriveFails(t *testing.T) {
	kept := map[string]Manifest{ManifestKey("acme-flow", "1.0.0"): {ID: "acme-flow", Version: "1.0.0"}}
	reg := &Registry{
		entries:  cloneManifests(kept),
		revision: "generation-1",
		derive:   func() (map[string]Manifest, error) { return nil, errDeriveFailed },
	}
	if _, err := reg.Get("acme-flow", "1.0.0"); err != nil {
		t.Fatalf("a failed derive must not empty the registry: %v", err)
	}
}

var errDeriveFailed = extpacks.ErrStockPack
