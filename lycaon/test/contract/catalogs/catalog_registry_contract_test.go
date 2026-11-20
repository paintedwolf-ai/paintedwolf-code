package contract

import (
	"github.com/lycaon/lycaon/internal/tools/toolstest"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/wiring"
)

// TestCatalogAllowlistRegistrySync builds the server through the production
// composition and requires every allowlisted tool to be registered on it.
func TestCatalogAllowlistRegistrySync(t *testing.T) {
	h := wiring.BuildForTest(t)
	registered := tools.RegisteredToolSet(h.ToolRegistry)
	if len(registered) == 0 {
		t.Fatal("production composition registered no tools")
	}
	contractcheck.FailErr(t, "toolstest.ValidateCatalogAllowlistSync", toolstest.ValidateCatalogAllowlistSync(registered))
}
