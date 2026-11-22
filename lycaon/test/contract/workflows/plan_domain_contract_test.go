package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPlanDomainIDsRegisteredOnBoot(t *testing.T) {
	t.Parallel()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	for _, id := range conditions.ShippedPlanDomainIDs() {
		if !reg.Has(id) {
			t.Fatalf("shipped plan domain id %q not registered", id)
		}
	}
}
