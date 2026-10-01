package extpacks_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/mcp/bindings"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
)

const widgetYAML = `id: fixture_widget
provider_id: fixture
tool_name: widget
schema:
  type: object
  required: [status, count]
  properties:
    status: { type: string }
    count: { type: integer }
fields:
  - key: widget_status
    type: string
    path: /status
  - key: widget_count
    type: int
    path: /count
  - key: is_ready
    type: bool
    path: /status
    equals: ready
`

const otherYAML = `id: other_widget
provider_id: fixture
tool_name: other
schema:
  type: object
  properties:
    x: { type: string }
fields:
  - key: other_x
    type: string
    path: /x
`

const dupKeyYAML = `id: dup_widget
provider_id: fixture
tool_name: dup
schema:
  type: object
  properties:
    status: { type: string }
fields:
  - key: widget_status
    type: string
    path: /status
`

func TestLoadEffectiveBindingsMergeAndDisable(t *testing.T) {
	dir := t.TempDir()
	writePackWithUnits(t, dir, "pack-a", "acme/bindings-a", map[string]string{
		"mcp_bindings/fixture_widget.yaml": widgetYAML,
	})
	writePackWithUnits(t, dir, "pack-b", "acme/bindings-b", map[string]string{
		"mcp_bindings/other_widget.yaml": otherYAML,
	})
	a := inventoryUnitPack(t, dir, "pack-a", "acme/bindings-a")
	b := inventoryUnitPack(t, dir, "pack-b", "acme/bindings-b")
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   []extpacks.PackContent{a, b},
		Desired: enabledExtensionPacks("acme/bindings-a", "acme/bindings-b"),
	})
	if !eff.HasLoaded(extpacks.MCPBindingUnitID("fixture_widget")) {
		t.Fatal("fixture_widget should load")
	}
	if !eff.HasLoaded(extpacks.MCPBindingUnitID("other_widget")) {
		t.Fatal("other_widget should load")
	}
	list, diags, err := extpacks.LoadEffectiveBindings(eff)
	testutil.FailErr(t, "LoadEffectiveBindings", err)
	if len(diags) != 0 {
		t.Fatalf("diags=%v", diags)
	}
	if len(list) != 2 {
		t.Fatalf("len=%d", len(list))
	}

	desired := extpacks.EmptyDesired()
	desired.Packs = []extpacks.DesiredPack{{ID: "acme/bindings-a"}, {ID: "acme/bindings-b"}}
	desired.Disabled = []string{extpacks.MCPBindingUnitID("other_widget")}
	eff2 := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   []extpacks.PackContent{a, b},
		Desired: desired,
	})
	if eff2.HasLoaded(extpacks.MCPBindingUnitID("other_widget")) {
		t.Fatal("disabled unit must not load")
	}
	list2, _, err := extpacks.LoadEffectiveBindings(eff2)
	testutil.FailErr(t, "LoadEffectiveBindings disabled", err)
	if len(list2) != 1 || list2[0].ID != "fixture_widget" {
		t.Fatalf("got %+v", list2)
	}
	for _, binding := range list2 {
		for _, f := range binding.Fields {
			if f.Key == "other_x" {
				t.Fatal("disabled binding fields must be absent")
			}
		}
	}
}

func TestLoadEffectiveBindingsDuplicateKey(t *testing.T) {
	dir := t.TempDir()
	writePackWithUnits(t, dir, "pack-a", "acme/bindings-a", map[string]string{
		"mcp_bindings/fixture_widget.yaml": widgetYAML,
	})
	writePackWithUnits(t, dir, "pack-b", "acme/bindings-b", map[string]string{
		"mcp_bindings/dup_widget.yaml": dupKeyYAML,
	})
	a := inventoryUnitPack(t, dir, "pack-a", "acme/bindings-a")
	b := inventoryUnitPack(t, dir, "pack-b", "acme/bindings-b")
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   []extpacks.PackContent{a, b},
		Desired: enabledExtensionPacks("acme/bindings-a", "acme/bindings-b"),
	})
	_, diags, err := extpacks.LoadEffectiveBindings(eff)
	if err == nil {
		t.Fatal("expected duplicate key error")
	}
	found := false
	for _, d := range diags {
		if d.Code == extpacks.DiagMCPBindingDupKey && strings.Contains(d.Message, "widget_status") {
			found = true
			if d.UnitID == "" {
				t.Fatal("diagnostic must include unit id")
			}
		}
	}
	if !found {
		t.Fatalf("expected dup key diag, got %v", diags)
	}
}

func TestValidateBindingDiagnosticIncludesUnitID(t *testing.T) {
	dir := t.TempDir()
	writePackWithUnits(t, dir, "pack-a", "acme/bindings-a", map[string]string{
		"mcp_bindings/fixture_widget.yaml": widgetYAML,
	})
	writePackWithUnits(t, dir, "pack-b", "acme/bindings-b", map[string]string{
		"mcp_bindings/dup_widget.yaml": dupKeyYAML,
	})
	a := inventoryUnitPack(t, dir, "pack-a", "acme/bindings-a")
	b := inventoryUnitPack(t, dir, "pack-b", "acme/bindings-b")
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   []extpacks.PackContent{a, b},
		Desired: enabledExtensionPacks("acme/bindings-a", "acme/bindings-b"),
	})
	_, diags, err := extpacks.LoadEffectiveBindings(eff)
	if err == nil {
		t.Fatal("expected error")
	}
	rep := &extpacks.ValidateReport{Diagnostics: diags, OK: false}
	text := extpacks.FormatValidateText(rep)
	if !strings.Contains(text, "mcp_bindings/") {
		t.Fatalf("validate text missing unit id: %s", text)
	}
	if !strings.Contains(text, extpacks.DiagMCPBindingDupKey) {
		t.Fatalf("validate text missing code: %s", text)
	}
	if !strings.Contains(text, "validate: FAIL") {
		t.Fatalf("want FAIL: %s", text)
	}
}

func TestGoldenConditionFromEffectiveBindings(t *testing.T) {
	dir := t.TempDir()
	writePackWithUnits(t, dir, "pack-a", "acme/bindings-a", map[string]string{
		"mcp_bindings/fixture_widget.yaml": widgetYAML,
	})
	a := inventoryUnitPack(t, dir, "pack-a", "acme/bindings-a")
	eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs:   []extpacks.PackContent{a},
		Desired: enabledExtensionPacks("acme/bindings-a"),
	})
	list, _, err := extpacks.LoadEffectiveBindings(eff)
	testutil.FailErr(t, "LoadEffectiveBindings", err)

	matched, fields, err := bindings.Apply(list, "fixture", "widget", `{"status":"ready","count":2}`)
	testutil.FailErr(t, "Apply", err)
	if !matched {
		t.Fatal("expected match")
	}
	gc := oar.NewGuardContext()
	gc.MCPSchemaMatched = matched
	gc.MCPFields = fields
	if !gc.MCPSchemaMatched || !oar.EvalMCPFieldBool(gc, "is_ready") {
		t.Fatal("expected mcp_schema_matched && is_ready")
	}
	if oar.EvalMCPFieldString(gc, "widget_status") != "ready" {
		t.Fatal("widget_status")
	}
	if oar.EvalMCPFieldInt(gc, "widget_count") != 2 {
		t.Fatal("widget_count")
	}

	matchedBad, _, err := bindings.Apply(list, "fixture", "widget", `not-json`)
	testutil.FailErr(t, "Apply bad", err)
	if matchedBad {
		t.Fatal("wrong JSON must not match")
	}
}

func enabledExtensionPacks(ids ...string) extpacks.DesiredState {
	desired := extpacks.EmptyDesired()
	for _, id := range ids {
		desired.Packs = append(desired.Packs, extpacks.DesiredPack{ID: id})
	}
	return desired
}

func writePackWithUnits(t *testing.T, root, leaf, id string, files map[string]string) {
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

func inventoryUnitPack(t *testing.T, root, leaf, id string) extpacks.PackContent {
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
