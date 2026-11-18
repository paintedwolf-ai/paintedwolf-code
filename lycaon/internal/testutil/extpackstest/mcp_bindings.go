package extpackstest

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/mcp/bindings"
	"github.com/lycaon/lycaon/internal/testutil"
)

// MCPBindings resolves the echo and widget bindings in the fixture pack.
func MCPBindings(t *testing.T) []bindings.Binding {
	t.Helper()
	root := extpacks.OnDisk(filepath.Join(testutil.CheckoutRoot(t), "lycaon", "internal", "testutil", "extpackstest", "testdata", "bindings"))
	manifest, err := extpacks.ManifestAt(root)
	testutil.FailErr(t, "load binding fixture manifest", err)
	pack, err := extpacks.InventoryPack(extpacks.Pack{ID: manifest.ID, Root: root}, manifest)
	testutil.FailErr(t, "inventory binding fixture pack", err)
	desired := extpacks.EmptyDesired()
	desired.Packs = []extpacks.DesiredPack{{ID: manifest.ID}}
	catalog := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: []extpacks.PackContent{pack}, Desired: desired})
	list, diags, err := extpacks.LoadEffectiveBindings(catalog)
	testutil.FailErr(t, "load effective fixture bindings", err)
	if len(diags) != 0 || len(list) != 2 {
		t.Fatalf("binding fixture resolved %d bindings with diagnostics %v", len(list), diags)
	}
	return list
}
