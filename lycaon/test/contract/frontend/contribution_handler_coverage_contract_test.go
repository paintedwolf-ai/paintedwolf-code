package contract

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/extpacks"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Native UI handlers and declarations have exact coverage.
func TestNativeHandlerInventoryCoverage(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)

	stock, err := extpacks.DiscoverStockContent()
	contractcheck.FailErr(t, "discover stock content", err)
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   stock,
		Desired: extpacks.EmptyDesired(),
	})
	contractcheck.FailErr(t, "stock boot", eff.BootError())
	view, err := catalogview.Build(t.Context(), filepath.Join(root, "lycaon"), eff)
	contractcheck.FailErr(t, "build stock view", err)

	denInventory, err := denNativeUIHandlerIDs(root)
	contractcheck.FailErr(t, "read Den handler inventory", err)

	errs := contribution.ValidateHandlerCoverage(view.Contributions, contribution.HandlerInventories{
		Den: denInventory,
	})
	for _, err := range errs {
		t.Error(err)
	}
}

// denNativeUIHandlerIDs reads the generated shell handler inventory.
func denNativeUIHandlerIDs(root string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(root, "lycaon-den", "src", "contributions",
		"native-ui-handlers.generated.ts"))
	if err != nil {
		return nil, err
	}
	body := regexp.MustCompile(`(?s)NATIVE_UI_HANDLER_IDS[^=]*=\s*\[(.*?)\]`).FindStringSubmatch(string(data))
	if body == nil {
		return nil, fmt.Errorf("no NATIVE_UI_HANDLER_IDS array in the generated inventory")
	}
	var out []string
	for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(body[1], -1) {
		out = append(out, m[1])
	}
	return out, nil
}
