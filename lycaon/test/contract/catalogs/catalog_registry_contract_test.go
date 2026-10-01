package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func TestCatalogAllowlistRegistrySync(t *testing.T) {
	text := contractcheck.ServeWireSource(t)
	for _, needle := range []string{
		"RegisterDelegationTools",
		"RegisterHandoffTools",
		"RegisterParseTools",
		"RegisterStateTools",
		"RegisterPlanTools",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("serve wire missing %q wiring", needle)
		}
	}

	reg := toolfixture.RegisterCatalogToolsForContract(t)
	if err := tools.ValidateCatalogAllowlistSync(toolfixture.BootRegisteredToolSet(t, reg)); err != nil {
		contractcheck.FailErr(t, "tools.ValidateCatalogAllowlistSync failed", err)
	}
}
