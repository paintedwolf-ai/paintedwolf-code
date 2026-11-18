package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestOARNextStepsArgsSchemaCheckedAtLoad loads the stock policy through a
// loader wired with the declared tool schemas, so every next-steps args and
// pick path is checked against the receiving tool's argument schema.
func TestOARNextStepsArgsSchemaCheckedAtLoad(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	contractcheck.FailErr(t, "install catalog", anchorcatalog.InstallFile(filepath.Join(root,
		"lycaon", "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")))
	eff := contractcheck.StockCatalog(t)
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	contractcheck.FailErr(t, "load tool schemas", err)
	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	contractcheck.FailErr(t, "oar loader", err)
	loader.SetToolSchemas(schemas)
	if _, err := loader.LoadEffectivePolicyWithCatalog(eff); err != nil {
		t.Fatalf("stock policy must load with args schema-checked: %v", err)
	}
}

// stepArgsPack stages a pack whose one policy unit points its next step at
// recall with the given argument path.
func stepArgsPack(t *testing.T, argPath string) extpacks.PackContent {
	t.Helper()
	const code = "ACME_STEP_ARGS"
	root := filepath.Join(t.TempDir(), "acme-step-args")
	files := map[string]string{
		"extension.yaml": "manifest_version: 1\nid: acme/step-args\nname: acme/step-args\nversion: 1.0.0\n" +
			"compatibility:\n  extension_api: ^1.0.0\ndependencies:\n  painted-wolf/platform:\n    version: ^1.0.0\n",
		"policy/" + code + ".yaml": "spec_version: '1.0'\nid: " + code + "\nkind: policy\non: tool.rejected\n" +
			"selector: {}\nrequires_facts: [paintedwolf.next_steps]\nwhen: 'false'\neffect: warn\n" +
			"cause: c\nwhy: w\nfix: 'f {{ paintedwolf.next_steps }}'\ninstead: i\n" +
			"x-paintedwolf-emit: rule:test\nmessage: m\n",
		"host/next-steps/" + code + ".yaml": "steps:\n  - tool: recall\n    args:\n      " + argPath +
			": x\n    say: 'Call {{ call }}.'\n",
	}
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		contractcheck.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(path), 0o755))
		contractcheck.FailErr(t, "write "+rel, os.WriteFile(path, []byte(body), 0o644))
	}
	man, err := extpacks.LoadManifest(root)
	contractcheck.FailErr(t, "load manifest", err)
	pc, err := extpacks.InventoryPack(extpacks.Pack{ID: man.ID, Root: extpacks.OnDisk(root)}, man)
	contractcheck.FailErr(t, "inventory pack", err)
	return pc
}

func buildViewWith(t *testing.T, pack extpacks.PackContent) error {
	t.Helper()
	stock, err := extpacks.DiscoverStockContent()
	contractcheck.FailErr(t, "discover stock content", err)
	desired := extpacks.EmptyDesired()
	desired.Packs = append(desired.Packs, extpacks.DesiredPack{ID: pack.Pack.ID})
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: append(stock, pack), Desired: desired})
	contractcheck.FailErr(t, "resolve with pack", eff.BootError())
	_, err = catalogview.Build(t.Context(), filepath.Join(contractcheck.RepoRoot(t), "lycaon"), eff)
	return err
}

// TestOARCatalogViewWiresToolSchemas builds the catalog view over a pack whose
// next step passes recall an argument recall declares, then one it does not:
// the view checks next-steps args against the tool schemas when it compiles rules.
func TestOARCatalogViewWiresToolSchemas(t *testing.T) {
	t.Parallel()
	if err := buildViewWith(t, stepArgsPack(t, "query")); err != nil {
		t.Fatalf("a next step with a declared argument must build: %v", err)
	}
	if err := buildViewWith(t, stepArgsPack(t, "no_such_argument")); err == nil {
		t.Fatal("a next step with an undeclared argument built")
	}
}
