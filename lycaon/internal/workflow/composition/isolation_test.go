package composition

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestManifestRequiresIsolationPackTopology(t *testing.T) {
	m := workflowdef.Manifest{Topology: "pack-probe"}
	if !manifestRequiresIsolation(m, extpackstest.StockCatalog(t)) {
		t.Fatal("pack topology should require isolation")
	}
}

func TestManifestRequiresIsolationParallelGroup(t *testing.T) {
	m := workflowdef.Manifest{PhaseDefs: []workflowdef.PhaseDef{{ID: "review_test", BindParallelGroup: []string{"review", "test"}}}}
	if !manifestRequiresIsolation(m, nil) {
		t.Fatal("parallel group should require isolation")
	}
}

func TestManifestRequiresIsolationPipelineDefault(t *testing.T) {
	m := workflowdef.Manifest{Topology: "default-pipeline"}
	if manifestRequiresIsolation(m, extpackstest.StockCatalog(t)) {
		t.Fatal("pipeline topology should not require isolation")
	}
}

// A nil catalog is the fail-closed case: no topology resolves, so only a
// manifest's own parallel-group binding can still demand isolation.
func TestManifestRequiresIsolationNilCatalog(t *testing.T) {
	if manifestRequiresIsolation(workflowdef.Manifest{Topology: "pack-probe"}, nil) {
		t.Fatal("nil catalog must not resolve a topology pattern")
	}
}
