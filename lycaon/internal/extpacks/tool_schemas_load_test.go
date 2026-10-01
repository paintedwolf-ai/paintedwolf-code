package extpacks_test

import (
	"github.com/lycaon/lycaon/internal/configlayout"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestLoadEffectiveToolSchemasStockParity(t *testing.T) {
	root := configlayout.FindModuleRoot()
	dir := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "tools", "schemas")
	want, err := toolschema.LoadSchemaDir(dir)
	testutil.FailErr(t, "LoadSchemaDir", err)

	content, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent", err)
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   content,
		Desired: extpacks.EmptyDesired(),
	})
	got, diags, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "LoadEffectiveToolSchemas", err)
	if len(diags) != 0 {
		t.Fatalf("diags=%v", diags)
	}
	if len(got.Tools) != len(want.Tools) {
		t.Fatalf("got %d tools want %d", len(got.Tools), len(want.Tools))
	}
	for name := range want.Tools {
		if _, ok := got.Tools[name]; !ok {
			t.Errorf("missing tool %q", name)
		}
	}
	if len(want.Tools) < 80 {
		t.Fatalf("expected stock schema set, got %d", len(want.Tools))
	}
}

func TestLoadEffectiveToolSchemasOwnOne(t *testing.T) {
	dir := t.TempDir()
	stockDesc := "stock read description"
	writeSchemaPack(t, dir, "platform", "painted-wolf/platform", map[string]string{
		"tools/schemas/read.yaml":  "description: " + stockDesc + "\nschema:\n  type: object\n",
		"tools/schemas/write.yaml": "description: stock write\nschema:\n  type: object\n",
	})
	selectedDesc := "selected read description only"
	writeSchemaPack(t, dir, "acme", "acme/schemas", map[string]string{
		"tools/schemas/read.yaml": "description: " + selectedDesc + "\nschema:\n  type: object\n",
	})
	platform := inventorySchemaPack(t, dir, "platform", "painted-wolf/platform")
	acme := inventorySchemaPack(t, dir, "acme", "acme/schemas")
	desired := extpacks.EmptyDesired()
	desired.Packs = []extpacks.DesiredPack{{ID: "acme/schemas"}}
	desired.Own = map[string]string{"tools/schemas/read": "acme/schemas"}
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   []extpacks.PackContent{platform, acme},
		Desired: desired,
	})
	cfg, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "LoadEffectiveToolSchemas", err)
	if cfg.Tools["read"].Description != selectedDesc {
		t.Fatalf("read desc=%q want selected", cfg.Tools["read"].Description)
	}
	if cfg.Tools["write"].Description != "stock write" {
		t.Fatalf("write desc changed: %q", cfg.Tools["write"].Description)
	}
}

// Disabling a schema removes custom copy while retaining the tool.
func TestLoadEffectiveToolSchemasDisableOneDegradesGracefully(t *testing.T) {
	dir := t.TempDir()
	writeSchemaPack(t, dir, "platform", "painted-wolf/platform", map[string]string{
		"tools/schemas/read.yaml":  "description: read\nschema:\n  type: object\n",
		"tools/schemas/write.yaml": "description: write\nschema:\n  type: object\n",
	})
	platform := inventorySchemaPack(t, dir, "platform", "painted-wolf/platform")
	desired := extpacks.EmptyDesired()
	desired.Disabled = []string{"tools/schemas/read"}
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   []extpacks.PackContent{platform},
		Desired: desired,
	})
	if eff.HasLoaded("tools/schemas/read") {
		t.Fatal("disabled unit must not load")
	}
	cfg, diags, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "LoadEffectiveToolSchemas", err)
	if _, ok := cfg.Tools["read"]; ok {
		t.Fatal("disabled tool must not carry a custom schema")
	}
	if _, ok := cfg.Tools["write"]; !ok {
		t.Fatal("write must still load")
	}
	assertToolSchemaMissingDiag(t, diags, "tools/schemas/read")
}

// Conflicting schemas remain installable so a provider can be selected.
func TestLoadEffectiveToolSchemasConflictDegradesGracefully(t *testing.T) {
	dir := t.TempDir()
	writeSchemaPack(t, dir, "platform", "painted-wolf/platform", map[string]string{
		"tools/schemas/read.yaml": "description: stock read\nschema:\n  type: object\n",
	})
	writeSchemaPack(t, dir, "acme", "acme/schemas", map[string]string{
		"tools/schemas/read.yaml": "description: acme read\nschema:\n  type: object\n",
	})
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs: []extpacks.PackContent{
			inventorySchemaPack(t, dir, "platform", "painted-wolf/platform"),
			inventorySchemaPack(t, dir, "acme", "acme/schemas"),
		},
		Desired: enabledExtensionPacks("acme/schemas"),
	})
	cfg, diags, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "LoadEffectiveToolSchemas", err)
	if _, ok := cfg.Tools["read"]; ok {
		t.Fatal("conflicted tool must not carry a schema until `own:` selects one")
	}
	assertToolSchemaMissingDiag(t, diags, "tools/schemas/read")

	// An `own:` selection installs the chosen schema.
	selected := extpacks.EmptyDesired()
	selected.Packs = []extpacks.DesiredPack{{ID: "acme/schemas"}}
	selected.Own = map[string]string{"tools/schemas/read": "acme/schemas"}
	effOwned := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs: []extpacks.PackContent{
			inventorySchemaPack(t, dir, "platform", "painted-wolf/platform"),
			inventorySchemaPack(t, dir, "acme", "acme/schemas"),
		},
		Desired: selected,
	})
	cfgOwned, _, err := extpacks.LoadEffectiveToolSchemas(effOwned)
	testutil.FailErr(t, "LoadEffectiveToolSchemas", err)
	if cfgOwned.Tools["read"].Description != "acme read" {
		t.Fatalf("read desc=%q want selected pack's schema", cfgOwned.Tools["read"].Description)
	}
}

// Winning malformed schema content is an author error and fails closed.
func TestLoadEffectiveToolSchemasInvalidContentFailsClosed(t *testing.T) {
	dir := t.TempDir()
	writeSchemaPack(t, dir, "acme", "acme/schemas", map[string]string{
		"tools/schemas/read.yaml": "not: [valid, schema\n",
	})
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   []extpacks.PackContent{inventorySchemaPack(t, dir, "acme", "acme/schemas")},
		Desired: enabledExtensionPacks("acme/schemas"),
	})
	cfg, diags, err := extpacks.LoadEffectiveToolSchemas(eff)
	if err == nil || cfg != nil {
		t.Fatalf("want fail closed on malformed content, got cfg=%v err=%v", cfg, err)
	}
	found := false
	for _, d := range diags {
		if d.Code == extpacks.DiagToolSchemaInvalid && d.UnitID == "tools/schemas/read" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %s diagnostic: %v", extpacks.DiagToolSchemaInvalid, diags)
	}
}

// A disable entry no pack provides is stale bookkeeping, not lost tool copy.
func TestLoadEffectiveToolSchemasIgnoresContributionlessDisable(t *testing.T) {
	dir := t.TempDir()
	writeSchemaPack(t, dir, "platform", "painted-wolf/platform", map[string]string{
		"tools/schemas/write.yaml": "description: write\nschema:\n  type: object\n",
	})
	desired := extpacks.EmptyDesired()
	desired.Disabled = []string{"tools/schemas/read"}
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   []extpacks.PackContent{inventorySchemaPack(t, dir, "platform", "painted-wolf/platform")},
		Desired: desired,
	})
	cfg, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	testutil.FailErr(t, "LoadEffectiveToolSchemas", err)
	if _, ok := cfg.Tools["write"]; !ok {
		t.Fatal("write must remain")
	}
}

func assertToolSchemaMissingDiag(t *testing.T, diags []extpacks.Diagnostic, unitID string) {
	t.Helper()
	for _, d := range diags {
		if d.Code == extpacks.DiagToolSchemaMissing && d.UnitID == unitID {
			return
		}
	}
	t.Fatalf("no %s diagnostic for %s: %v", extpacks.DiagToolSchemaMissing, unitID, diags)
}

func TestNoToolSchemasMonolith(t *testing.T) {
	root := configlayout.FindModuleRoot()
	monolith := "tool" + "-schemas.yaml"
	path := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "tools", monolith)
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("monolith must not exist: %s", path)
	}
	_, _, err := extpacks.LoadEffectiveToolSchemas(nil)
	if err == nil || !strings.Contains(err.Error(), "effective catalog required") {
		t.Fatalf("nil eff err=%v", err)
	}
}

func writeSchemaPack(t *testing.T, root, leaf, id string, files map[string]string) {
	t.Helper()
	packRoot := filepath.Join(root, leaf)
	if err := os.MkdirAll(packRoot, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	man := "manifest_version: 1\nid: " + id + "\nname: fixture\nversion: 1.0.0\ncompatibility:\n  extension_api: ^1.0.0\n"
	if err := os.WriteFile(filepath.Join(packRoot, "extension.yaml"), []byte(man), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	for rel, body := range files {
		path := filepath.Join(packRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			testutil.FailErr(t, "create directory", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
}

func inventorySchemaPack(t *testing.T, root, leaf, id string) extpacks.PackContent {
	t.Helper()
	pc, err := extpacks.InventoryPack(extpacks.Pack{
		ID:   id,
		Root: extpacks.OnDisk(filepath.Join(root, leaf)),
	}, extpacks.Manifest{
		ID:            id,
		Compatibility: extpacks.ManifestCompatibility{ExtensionAPI: "^1.0.0"},
	})
	testutil.FailErr(t, "InventoryPack", err)
	return pc
}
