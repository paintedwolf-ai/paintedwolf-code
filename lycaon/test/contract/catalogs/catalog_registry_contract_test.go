package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/wiring"
)

// TestCatalogAllowlistRegistrySync builds the server through the production
// composition and requires every allowlisted tool to be registered on it.
func TestCatalogAllowlistRegistrySync(t *testing.T) {
	h := wiring.BuildForTest(t)
	registered, err := tools.BootRegisteredToolSet(h.ToolRegistry)
	contractcheck.FailErr(t, "tools.BootRegisteredToolSet", err)
	if len(registered) == 0 {
		t.Fatal("production composition registered no tools")
	}
	contractcheck.FailErr(t, "tools.ValidateCatalogAllowlistSync", tools.ValidateCatalogAllowlistSync(registered))
}
