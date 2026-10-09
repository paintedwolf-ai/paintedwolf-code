package website

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"gopkg.in/yaml.v3"
)

// completeSDKOverlay builds an overlay with prose for every engine id so the
// merge succeeds against the real catalog. Tests mutate it to prove drift
// fails closed.
func completeSDKOverlay(t *testing.T, moduleRoot string) *SDKOverlay {
	t.Helper()
	overlay := &SDKOverlay{
		Gates:                map[string]SDKOverlayEntry{},
		UnitKinds:            map[string]SDKOverlayUnitKind{},
		Packs:                map[string]SDKOverlayEntry{},
		ExtensionDiagnostics: map[string]SDKOverlayEntry{},
		OARFacts:             map[string]SDKOverlayEntry{},
		OARFunctions:         map[string]SDKOverlayEntry{},
		Surfaces:             map[string]SDKOverlayEntry{},
	}
	surfaces, err := loadCoordinatorSurfaces(moduleRoot)
	if err != nil {
		t.Fatalf("load coordinator surfaces: %v", err)
	}
	for _, s := range surfaces {
		overlay.Surfaces[s.ID] = SDKOverlayEntry{Description: "surface " + s.ID}
	}
	overlay.WorkflowManifest = map[string]SDKOverlayEntry{}
	for _, f := range workflowdef.ManifestFieldInventory() {
		overlay.WorkflowManifest[f.Path] = SDKOverlayEntry{Description: "field " + f.Path}
	}
	static, prefixes, _ := workflow.PublicGateKit()
	for _, id := range append(append([]string(nil), static...), prefixes...) {
		overlay.Gates[id] = SDKOverlayEntry{Description: "gate " + id}
	}
	for _, id := range extpacks.UnitKindRoots() {
		overlay.UnitKinds[id] = SDKOverlayUnitKind{UnitIDForm: id + "/<name>", Description: "kind " + id}
	}
	stock, err := extpacks.DiscoverStockContent()
	if err != nil {
		t.Fatalf("discover stock: %v", err)
	}
	for _, pc := range stock {
		overlay.Packs[pc.Manifest.ID] = SDKOverlayEntry{Description: "pack " + pc.Manifest.ID}
	}
	for _, code := range extpacks.AllDiagnosticCodes() {
		overlay.ExtensionDiagnostics[code] = SDKOverlayEntry{Description: "diag " + code}
	}
	for _, f := range oar.FactCatalogue() {
		overlay.OARFacts[f.Name] = SDKOverlayEntry{Description: "fact " + f.Name}
	}
	for _, f := range oar.ObservationFunctionCatalogue() {
		overlay.OARFunctions[f.Name] = SDKOverlayEntry{Description: "fn " + f.Name}
	}
	return overlay
}

func TestMergeSDK_RealCatalogComplete(t *testing.T) {
	root := repoLycaonRoot(t)
	out, err := MergeSDK(root, completeSDKOverlay(t, root))
	if err != nil {
		t.Fatalf("MergeSDK: %v", err)
	}
	static, prefixes, bundledOnly := workflow.PublicGateKit()
	if len(out.GateKit.Static) != len(static) {
		t.Fatalf("static gates = %d want %d", len(out.GateKit.Static), len(static))
	}
	if len(out.GateKit.Parameterized) != len(prefixes) {
		t.Fatalf("parameterized gates = %d want %d", len(out.GateKit.Parameterized), len(prefixes))
	}
	if len(out.GateKit.BundledOnly) != len(bundledOnly) {
		t.Fatalf("bundled-only gates = %d want %d", len(out.GateKit.BundledOnly), len(bundledOnly))
	}
	if len(out.UnitKinds) != len(extpacks.UnitKindRoots()) {
		t.Fatalf("unit kinds = %d want %d", len(out.UnitKinds), len(extpacks.UnitKindRoots()))
	}
	for _, kind := range out.UnitKinds {
		if kind.ProjectScope == "" {
			t.Fatalf("unit kind %q missing project_scope", kind.ID)
		}
		wantOwnable := !extpacks.ProviderScoped(kind.ID)
		if kind.Ownable != wantOwnable {
			t.Fatalf("unit kind %q ownable = %v want %v", kind.ID, kind.Ownable, wantOwnable)
		}
		if kind.ID == "host/detection-packs" && kind.ProjectScope != "device_only" {
			t.Fatalf("detection packs project_scope = %q want device_only", kind.ProjectScope)
		}
		if kind.ID == "workflows" && kind.ProjectScope != "shared" {
			t.Fatalf("workflows project_scope = %q want shared", kind.ProjectScope)
		}
		if kind.ID == "policy" && kind.ProjectScope != "additive" {
			t.Fatalf("policy project_scope = %q want additive", kind.ProjectScope)
		}
	}
	if len(out.Packs) == 0 {
		t.Fatal("expected stock packs")
	}
	if len(out.WorkflowDiagnostics) == 0 {
		t.Fatal("expected workflow diagnostics")
	}
	for _, d := range out.WorkflowDiagnostics {
		if strings.TrimSpace(d.Message) == "" {
			t.Fatalf("workflow diagnostic %q has empty message", d.Code)
		}
	}
	if len(out.ExtensionDiagnostics) != len(extpacks.AllDiagnosticCodes()) {
		t.Fatalf("extension diagnostics = %d want %d", len(out.ExtensionDiagnostics), len(extpacks.AllDiagnosticCodes()))
	}
	if len(out.OAR.Facts) != len(oar.FactCatalogue()) {
		t.Fatalf("OAR facts = %d want %d", len(out.OAR.Facts), len(oar.FactCatalogue()))
	}
	if len(out.OAR.Functions) != len(oar.ObservationFunctionCatalogue()) {
		t.Fatalf("OAR functions = %d want %d", len(out.OAR.Functions), len(oar.ObservationFunctionCatalogue()))
	}
	if want := nativeToolManifestCount(t, root); len(out.Tools) != want {
		t.Fatalf("native tools = %d want %d", len(out.Tools), want)
	}
	for _, tool := range out.Tools {
		if strings.TrimSpace(tool.Description) == "" || strings.TrimSpace(tool.Family) == "" {
			t.Fatalf("tool %q projected without family or description", tool.Name)
		}
		if tool.Name == "command" {
			t.Fatal("command is not a native manifest tool — it must not be synthesized into the projection")
		}
	}
	if want := anchorCatalogCount(t, root); len(out.Anchors) != want {
		t.Fatalf("anchors = %d want %d", len(out.Anchors), want)
	}
	surfaces, err := loadCoordinatorSurfaces(root)
	if err != nil {
		t.Fatalf("load coordinator surfaces: %v", err)
	}
	if len(out.Surfaces) != len(surfaces) {
		t.Fatalf("surfaces = %d want %d", len(out.Surfaces), len(surfaces))
	}
	for i, s := range out.Surfaces {
		if s.ID != surfaces[i].ID {
			t.Fatalf("surface[%d] = %q want %q (declaration order)", i, s.ID, surfaces[i].ID)
		}
		if strings.TrimSpace(s.Description) == "" || len(s.Tools) == 0 {
			t.Fatalf("surface %q projected without description or tools", s.ID)
		}
	}
}

// nativeToolManifestCount counts ids in the native manifest independently of
// the loader, so a loader that silently dropped a family would fail the
// completeness assertion.
func nativeToolManifestCount(t *testing.T, moduleRoot string) int {
	t.Helper()
	path := filepath.Join(moduleRoot, "config", "packs", "painted-wolf", "platform", "tools", "native-tools.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read native tool manifest: %v", err)
	}
	var doc struct {
		Native map[string][]string `yaml:"native"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse native tool manifest: %v", err)
	}
	total := 0
	for _, ids := range doc.Native {
		total += len(ids)
	}
	return total
}

// anchorCatalogCount counts catalog rows independently of the loader.
func anchorCatalogCount(t *testing.T, moduleRoot string) int {
	t.Helper()
	path := filepath.Join(moduleRoot, "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read anchor catalog: %v", err)
	}
	var doc struct {
		Anchors []map[string]any `yaml:"anchors"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse anchor catalog: %v", err)
	}
	return len(doc.Anchors)
}

func TestMergeSDK_WorkflowManifestMatchesInventory(t *testing.T) {
	root := repoLycaonRoot(t)
	out, err := MergeSDK(root, completeSDKOverlay(t, root))
	if err != nil {
		t.Fatalf("MergeSDK: %v", err)
	}
	inventory := workflowdef.ManifestFieldInventory()
	if len(out.WorkflowManifest) != len(inventory) {
		t.Fatalf("workflow manifest fields = %d want %d", len(out.WorkflowManifest), len(inventory))
	}
	for i, f := range inventory {
		if out.WorkflowManifest[i].Path != f.Path {
			t.Fatalf("field[%d] = %q want %q (inventory order)", i, out.WorkflowManifest[i].Path, f.Path)
		}
	}
}

func TestMergeSDK_MissingManifestFieldProseRejected(t *testing.T) {
	root := repoLycaonRoot(t)
	overlay := completeSDKOverlay(t, root)
	delete(overlay.WorkflowManifest, "phases.id")
	_, err := MergeSDK(root, overlay)
	if err == nil || !strings.Contains(err.Error(), `workflow manifest field "phases.id"`) {
		t.Fatalf("expected missing-prose error for phases.id, got %v", err)
	}
}

func TestMergeSDK_StaleOverlayManifestFieldRejected(t *testing.T) {
	root := repoLycaonRoot(t)
	overlay := completeSDKOverlay(t, root)
	overlay.WorkflowManifest["ghost.path"] = SDKOverlayEntry{Description: "no such field"}
	_, err := MergeSDK(root, overlay)
	if err == nil || !strings.Contains(err.Error(), "ghost.path") {
		t.Fatalf("expected stale-overlay error for ghost.path, got %v", err)
	}
}

// TestMergeSDK_TutorialProjectsPackBodies pins that the tutorial page renders
// the tested fixture's bytes: the manifest first, every file present, no empties.
func TestMergeSDK_TutorialProjectsPackBodies(t *testing.T) {
	root := repoLycaonRoot(t)
	out, err := MergeSDK(root, completeSDKOverlay(t, root))
	if err != nil {
		t.Fatalf("MergeSDK: %v", err)
	}
	if len(out.Tutorial) < 4 {
		t.Fatalf("tutorial files = %d want at least 4", len(out.Tutorial))
	}
	if out.Tutorial[0].Path != "extension.yaml" {
		t.Fatalf("first tutorial file = %q want extension.yaml", out.Tutorial[0].Path)
	}
	want := map[string]bool{
		"extension.yaml":                          false,
		"workflows/triage-lite/workflow.yaml":     false,
		"policy/TRIAGE_LITE_WRITE_FORBIDDEN.yaml": false,
		"guidance/triage-lite-plan.md":            false,
	}
	for _, f := range out.Tutorial {
		if strings.TrimSpace(f.Body) == "" {
			t.Fatalf("tutorial file %q projected empty", f.Path)
		}
		if _, ok := want[f.Path]; ok {
			want[f.Path] = true
		}
	}
	for path, seen := range want {
		if !seen {
			t.Fatalf("tutorial projection is missing %q", path)
		}
	}
}

func TestLoadTutorialPack_EmptyDirRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "config", "fixtures", "tutorial-pack"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, err := loadTutorialPack(dir)
	if err == nil || !strings.Contains(err.Error(), "no files") {
		t.Fatalf("expected empty-pack error, got %v", err)
	}
}

func TestMergeSDK_MissingSurfaceProseRejected(t *testing.T) {
	root := repoLycaonRoot(t)
	overlay := completeSDKOverlay(t, root)
	delete(overlay.Surfaces, "implement_routing")
	_, err := MergeSDK(root, overlay)
	if err == nil || !strings.Contains(err.Error(), `coordinator surface "implement_routing"`) {
		t.Fatalf("expected missing-prose error for implement_routing, got %v", err)
	}
}

func TestMergeSDK_StaleOverlaySurfaceRejected(t *testing.T) {
	root := repoLycaonRoot(t)
	overlay := completeSDKOverlay(t, root)
	overlay.Surfaces["ghost_surface"] = SDKOverlayEntry{Description: "no such surface"}
	_, err := MergeSDK(root, overlay)
	if err == nil || !strings.Contains(err.Error(), "ghost_surface") {
		t.Fatalf("expected stale-overlay error for ghost_surface, got %v", err)
	}
}

// TestLoadNativeToolCatalog_MissingSchemaCopyRejected proves the two engine
// tool files must agree: a manifest id with no schema description is a build
// error, not a docs row with a blank meaning column.
func TestLoadNativeToolCatalog_MissingSchemaCopyRejected(t *testing.T) {
	dir := t.TempDir()
	toolsDir := filepath.Join(dir, "config", "packs", "painted-wolf", "platform", "tools")
	if err := os.MkdirAll(toolsDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifest := "native:\n  filesystem:\n    - read\n    - ghost_tool\n"
	if err := os.WriteFile(filepath.Join(toolsDir, "native-tools.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	schemaDir := filepath.Join(toolsDir, "schemas")
	if err := os.MkdirAll(schemaDir, 0o750); err != nil {
		t.Fatalf("mkdir schemas: %v", err)
	}
	if err := os.WriteFile(filepath.Join(schemaDir, "read.yaml"), []byte("description: Read a file.\n"), 0o600); err != nil {
		t.Fatalf("write schema unit: %v", err)
	}
	_, err := loadNativeToolCatalog(dir)
	if err == nil || !strings.Contains(err.Error(), "ghost_tool") {
		t.Fatalf("expected missing-schema-copy error for ghost_tool, got %v", err)
	}
}

// TestLoadAnchorCatalog_IncompleteRowRejected proves an anchor cannot reach the
// docs as a bare id: every rendered column is required copy.
func TestLoadAnchorCatalog_IncompleteRowRejected(t *testing.T) {
	dir := t.TempDir()
	anchorsDir := filepath.Join(dir, "config", "packs", "painted-wolf", "platform", "host", "anchors")
	if err := os.MkdirAll(anchorsDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "anchors:\n  - id: gate.blocked\n    title: Gate blocked\n    trigger_class: tool_rejection\n    surface: coordinator\n"
	if err := os.WriteFile(filepath.Join(anchorsDir, "catalog.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	_, err := loadAnchorCatalog(dir)
	if err == nil || !strings.Contains(err.Error(), "no description") {
		t.Fatalf("expected missing-description error, got %v", err)
	}
}

func TestMergeSDK_MissingFactProseRejected(t *testing.T) {
	root := repoLycaonRoot(t)
	overlay := completeSDKOverlay(t, root)
	delete(overlay.OARFacts, "tool")
	_, err := MergeSDK(root, overlay)
	if err == nil || !strings.Contains(err.Error(), `OAR fact "tool"`) {
		t.Fatalf("expected missing-prose error for fact tool, got %v", err)
	}
}

func TestMergeSDK_StaleOverlayFactRejected(t *testing.T) {
	root := repoLycaonRoot(t)
	overlay := completeSDKOverlay(t, root)
	overlay.OARFacts["ghost_fact"] = SDKOverlayEntry{Description: "no such fact"}
	_, err := MergeSDK(root, overlay)
	if err == nil || !strings.Contains(err.Error(), "ghost_fact") {
		t.Fatalf("expected stale-overlay error for ghost_fact, got %v", err)
	}
}

func TestMergeSDK_MissingGateProseRejected(t *testing.T) {
	root := repoLycaonRoot(t)
	overlay := completeSDKOverlay(t, root)
	delete(overlay.Gates, "gates_satisfied")
	_, err := MergeSDK(root, overlay)
	if err == nil || !strings.Contains(err.Error(), "gates_satisfied") {
		t.Fatalf("expected missing-prose error for gates_satisfied, got %v", err)
	}
}

func TestMergeSDK_StaleOverlayGateRejected(t *testing.T) {
	root := repoLycaonRoot(t)
	overlay := completeSDKOverlay(t, root)
	overlay.Gates["ghost_gate"] = SDKOverlayEntry{Description: "no such gate"}
	_, err := MergeSDK(root, overlay)
	if err == nil || !strings.Contains(err.Error(), "ghost_gate") {
		t.Fatalf("expected stale-overlay error for ghost_gate, got %v", err)
	}
}

func TestMergeSDK_StaleOverlayPackRejected(t *testing.T) {
	root := repoLycaonRoot(t)
	overlay := completeSDKOverlay(t, root)
	overlay.Packs["acme/ghost"] = SDKOverlayEntry{Description: "no such pack"}
	_, err := MergeSDK(root, overlay)
	if err == nil || !strings.Contains(err.Error(), "acme/ghost") {
		t.Fatalf("expected stale-overlay error for acme/ghost, got %v", err)
	}
}

// TestMergeSDK_PosturesMatchRegistry pins the projection to the same posture set
// the host validates, so a posture added to the catalog cannot reach users
// without reaching the SDK docs in the same change.
func TestMergeSDK_PosturesMatchRegistry(t *testing.T) {
	root := repoLycaonRoot(t)
	out, err := MergeSDK(root, completeSDKOverlay(t, root))
	if err != nil {
		t.Fatalf("MergeSDK: %v", err)
	}
	want := profiles.AllSessionPostures()
	if len(out.Postures) != len(want) {
		t.Fatalf("postures = %d want %d", len(out.Postures), len(want))
	}
	for i, p := range want {
		if out.Postures[i].ID != string(p) {
			t.Fatalf("posture[%d] = %q want %q (declaration order)", i, out.Postures[i].ID, p)
		}
		if strings.TrimSpace(out.Postures[i].Description) == "" {
			t.Fatalf("posture %q has empty description", p)
		}
	}
}

// TestLoadPostureCatalog_IncompleteEntryRejected proves the projection fails
// closed rather than shipping a posture row with no docs copy.
func TestLoadPostureCatalog_IncompleteEntryRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config", "packs", "painted-wolf", "platform", "host")
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "postures:\n  spec:\n    label: Spec\n"
	if err := os.WriteFile(filepath.Join(path, "session-postures.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	_, err := loadPostureCatalog(dir)
	if err == nil || !strings.Contains(err.Error(), "no description") {
		t.Fatalf("expected missing-description error, got %v", err)
	}
}

// TestRenderSDKYAML_RealCatalog round-trips the rendered document so the Hugo
// side always receives parseable YAML with the projected sections present.
func TestRenderSDKYAML_RealCatalog(t *testing.T) {
	root := repoLycaonRoot(t)
	overlay := completeSDKOverlay(t, root)
	raw, err := yaml.Marshal(overlay)
	if err != nil {
		t.Fatalf("marshal overlay: %v", err)
	}
	path := filepath.Join(t.TempDir(), "sdk.overlay.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	body, err := RenderSDKYAML(root, path)
	if err != nil {
		t.Fatalf("RenderSDKYAML: %v", err)
	}
	if !strings.HasPrefix(string(body), "# Codegened from paintedwolf-ai/lycaon") {
		t.Fatal("missing generated header")
	}
	var round WebsiteSDK
	if err := yaml.Unmarshal(body, &round); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if len(round.GateKit.Static) == 0 || len(round.UnitKinds) == 0 || len(round.Packs) == 0 {
		t.Fatal("round-trip lost projected sections")
	}
	if len(round.Tools) == 0 || len(round.Anchors) == 0 || len(round.Surfaces) == 0 {
		t.Fatal("round-trip lost the tool, anchor, or surface vocabulary")
	}
	if len(round.WorkflowManifest) == 0 {
		t.Fatal("round-trip lost the workflow manifest reference")
	}
}
