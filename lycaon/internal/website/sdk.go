// SDK contract projection for paintedwolf.ai. Engine vocabularies are projected
// verbatim from their declarations; the website overlay supplies prose only, and
// overlay completeness is checked in both directions.
package website

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"gopkg.in/yaml.v3"
)

const sdkGeneratedHeader = `# Codegened from paintedwolf-ai/lycaon — do not hand-edit.
# Source: engine vocabularies (workflow gate kit, extpacks unit kinds + stock
#   inventory + resolve diagnostics, workflow-diagnostics catalog, OAR fact
#   catalogue + observation functions, native tool manifest + tool schema units,
#   anchor catalog, coordinator surfaces, workflow manifest field inventory,
#   tutorial fixture pack)
#   + data/sdk.overlay.yaml
# Regenerate: ./task sdk:sync (LYCAON_ROOT checkout must match data/paintedwolf.ref).
#
# Machine ids come from the engine; descriptions come from the website overlay.
# The projection fails closed when either side drifts: an engine id with no
# overlay prose, or overlay prose for an id the engine no longer ships.
`

// WebsiteSDK is the Hugo data/sdk.yaml shape.
type WebsiteSDK struct {
	GateKit              SDKGateKit      `yaml:"gate_kit"`
	Postures             []SDKPosture    `yaml:"postures"`
	UnitKinds            []SDKUnitKind   `yaml:"unit_kinds"`
	Packs                []SDKPack       `yaml:"packs"`
	WorkflowDiagnostics  []SDKDiagnostic `yaml:"workflow_diagnostics"`
	ExtensionDiagnostics []SDKDiagnostic `yaml:"extension_diagnostics"`
	OAR                  SDKOAR          `yaml:"oar"`
	Tools                []SDKTool       `yaml:"tools"`
	Anchors              []SDKAnchor     `yaml:"anchors"`
	Surfaces             []SDKSurface    `yaml:"surfaces"`

	WorkflowManifest []SDKManifestField `yaml:"workflow_manifest"`
	Tutorial         []SDKTutorialFile  `yaml:"tutorial"`
}

// SDKTutorialFile is one tutorial-pack file body, projected verbatim so the
// code blocks on the tutorial page are the bytes the engine's own contract test
// loads — a page block and a rotted example cannot diverge.
type SDKTutorialFile struct {
	Path string `yaml:"path"` // pack-relative, e.g. workflows/triage-lite/workflow.yaml
	Body string `yaml:"body"`
}

// SDKManifestField is one workflow-manifest field with docs copy. The rows come
// from the loader's own struct definitions by reflection, so a field the loader
// starts parsing cannot stay undocumented.
type SDKManifestField struct {
	Section     string `yaml:"section"`
	Key         string `yaml:"key"`
	Path        string `yaml:"path"`
	Type        string `yaml:"type"`
	Description string `yaml:"description"`
}

// SDKTool is one native tool id with its engine-managed description. The id set
// comes from the native manifest and the copy from the tool schemas, so the two
// engine files must agree before the docs can ship.
type SDKTool struct {
	Name        string `yaml:"name"`
	Family      string `yaml:"family"`
	Description string `yaml:"description"`
}

// SDKAnchor is one lifecycle anchor from the host catalog — the vocabulary both
// rule `on:` and inform bindings select on. All copy is engine-managed.
type SDKAnchor struct {
	ID           string   `yaml:"id"`
	Title        string   `yaml:"title"`
	Description  string   `yaml:"description"`
	TriggerClass string   `yaml:"trigger_class"`
	Surface      string   `yaml:"surface"`
	Planes       []string `yaml:"planes"`
}

// SDKSurface is one coordinator capability surface with website prose. Tool
// lists and the exit class come from the surface catalog; the description
// comes from the overlay.
type SDKSurface struct {
	ID          string   `yaml:"id"`
	Exit        string   `yaml:"exit,omitempty"`
	Tools       []string `yaml:"tools"`
	Description string   `yaml:"description"`
}

// SDKOAR is the Open Agent Rules condition vocabulary.
type SDKOAR struct {
	Facts     []SDKFact         `yaml:"facts"`
	Functions []SDKFactFunction `yaml:"functions"`
}

// SDKFact is one typed fact with documentation.
type SDKFact struct {
	Name          string `yaml:"name"`
	Type          string `yaml:"type"`
	Class         string `yaml:"class"`
	Tier          string `yaml:"tier"`
	Profile       string `yaml:"profile,omitempty"`
	PublishedName string `yaml:"published_name,omitempty"`
	Description   string `yaml:"description"`
}

// SDKFactFunction is one parameterized observation function with docs copy.
type SDKFactFunction struct {
	Name          string `yaml:"name"`
	Signature     string `yaml:"signature"`
	Tier          string `yaml:"tier"`
	Profile       string `yaml:"profile,omitempty"`
	PublishedName string `yaml:"published_name,omitempty"`
	Description   string `yaml:"description"`
}

// SDKPosture is one session posture — the coordinator permission bucket a
// workflow selects with initial_posture / on_enter.set_posture. Label and
// description come from session-postures.yaml, not the website overlay.
type SDKPosture struct {
	ID          string `yaml:"id"`
	Label       string `yaml:"label,omitempty"`
	Description string `yaml:"description"`
}

// SDKGateKit is the custom-workflow gate vocabulary.
type SDKGateKit struct {
	Static        []SDKGate `yaml:"static"`
	Parameterized []SDKGate `yaml:"parameterized"`
	BundledOnly   []string  `yaml:"bundled_only"`
}

// SDKGate is one composable gate leaf (or parameterized prefix).
type SDKGate struct {
	ID          string `yaml:"id"`
	Description string `yaml:"description"`
}

// SDKUnitKind is one pack directory root inventoried as provide units.
type SDKUnitKind struct {
	ID           string `yaml:"id"`
	UnitIDForm   string `yaml:"unit_id_form"`
	ProjectScope string `yaml:"project_scope"` // shared | additive | device_only
	Ownable      bool   `yaml:"ownable"`
	Description  string `yaml:"description"`
}

// SDKPack is one stock pack row.
type SDKPack struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Feature     string   `yaml:"feature,omitempty"`
	Requires    []string `yaml:"requires,omitempty"`
	Units       int      `yaml:"units"`
	Description string   `yaml:"description"`
}

// SDKDiagnostic is one stable diagnostic code with docs copy.
type SDKDiagnostic struct {
	Code        string `yaml:"code"`
	Message     string `yaml:"message,omitempty"`
	Replacement string `yaml:"replacement,omitempty"`
	Description string `yaml:"description,omitempty"`
	// Severity is the engine's classification, so the published table says
	// which diagnostics fail an authoring gate without anyone restating it.
	Severity string `yaml:"severity,omitempty"`
}

// SDKOverlay is the hand-maintained paintedwolf-www prose overlay.
type SDKOverlay struct {
	Gates                map[string]SDKOverlayEntry    `yaml:"gates"`
	UnitKinds            map[string]SDKOverlayUnitKind `yaml:"unit_kinds"`
	Packs                map[string]SDKOverlayEntry    `yaml:"packs"`
	ExtensionDiagnostics map[string]SDKOverlayEntry    `yaml:"extension_diagnostics"`
	OARFacts             map[string]SDKOverlayEntry    `yaml:"oar_facts"`
	OARFunctions         map[string]SDKOverlayEntry    `yaml:"oar_functions"`
	Surfaces             map[string]SDKOverlayEntry    `yaml:"surfaces"`
	WorkflowManifest     map[string]SDKOverlayEntry    `yaml:"workflow_manifest"`
}

// SDKOverlayEntry is website prose for one machine id.
type SDKOverlayEntry struct {
	Description string `yaml:"description"`
}

// SDKOverlayUnitKind adds the unit-id derivation form to the prose.
type SDKOverlayUnitKind struct {
	UnitIDForm  string `yaml:"unit_id_form"`
	Description string `yaml:"description"`
}

// workflowDiagCopy is one shared/workflow-diagnostics/<code>.yaml document.
type workflowDiagCopy struct {
	Code        string `yaml:"code"`
	Message     string `yaml:"message"`
	Replacement string `yaml:"replacement"`
}

// RenderSDKYAML projects the engine extension vocabularies plus the website
// overlay into the Hugo data/sdk.yaml document.
func RenderSDKYAML(moduleRoot, overlayPath string) ([]byte, error) {
	overlay, err := LoadSDKOverlay(overlayPath)
	if err != nil {
		return nil, fmt.Errorf("load overlay: %w", err)
	}
	out, err := MergeSDK(moduleRoot, overlay)
	if err != nil {
		return nil, err
	}
	body, err := yaml.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal sdk: %w", err)
	}
	return append([]byte(sdkGeneratedHeader), body...), nil
}

// LoadSDKOverlay reads data/sdk.overlay.yaml.
func LoadSDKOverlay(path string) (*SDKOverlay, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path from caller (www sync script)
	if err != nil {
		return nil, err
	}
	var overlay SDKOverlay
	if err := yaml.Unmarshal(data, &overlay); err != nil {
		return nil, err
	}
	return &overlay, nil
}

// MergeSDK combines engine vocabularies with overlay prose, failing closed on
// drift in either direction.
func MergeSDK(moduleRoot string, overlay *SDKOverlay) (*WebsiteSDK, error) {
	if overlay == nil {
		return nil, fmt.Errorf("overlay is nil")
	}
	out := &WebsiteSDK{}

	gateKit, err := mergeGateKit(overlay)
	if err != nil {
		return nil, err
	}
	out.GateKit = gateKit

	postures, err := loadPostureCatalog(moduleRoot)
	if err != nil {
		return nil, err
	}
	out.Postures = postures

	unitKinds, err := mergeUnitKinds(overlay)
	if err != nil {
		return nil, err
	}
	out.UnitKinds = unitKinds

	packs, err := mergeStockPacks(moduleRoot, overlay)
	if err != nil {
		return nil, err
	}
	out.Packs = packs

	diags, err := loadWorkflowDiagCopy(moduleRoot)
	if err != nil {
		return nil, err
	}
	out.WorkflowDiagnostics = diags
	if err := mergeOARSDK(out, overlay); err != nil {
		return nil, err
	}
	if err := mergeExtensionDiagnosticsSDK(out, overlay); err != nil {
		return nil, err
	}

	tools, err := loadNativeToolCatalog(moduleRoot)
	if err != nil {
		return nil, err
	}
	out.Tools = tools

	anchors, err := loadAnchorCatalog(moduleRoot)
	if err != nil {
		return nil, err
	}
	out.Anchors = anchors

	surfaces, err := mergeCoordinatorSurfaces(moduleRoot, overlay)
	if err != nil {
		return nil, err
	}
	out.Surfaces = surfaces

	if err := mergeWorkflowManifestSDK(out, overlay); err != nil {
		return nil, err
	}

	tutorial, err := loadTutorialPack(moduleRoot)
	if err != nil {
		return nil, err
	}
	out.Tutorial = tutorial
	return out, nil
}

// mergeGateKit validates website prose against the engine-managed gate kit while
// preserving its static, parameterized, and bundled-only partitions.
func mergeGateKit(overlay *SDKOverlay) (SDKGateKit, error) {
	static, prefixes, bundledOnly := workflow.PublicGateKit()
	seen := map[string]bool{}
	kit := SDKGateKit{BundledOnly: bundledOnly}
	for _, id := range static {
		gate, err := overlayGate(overlay, id)
		if err != nil {
			return SDKGateKit{}, err
		}
		seen[id] = true
		kit.Static = append(kit.Static, gate)
	}
	for _, id := range prefixes {
		gate, err := overlayGate(overlay, id)
		if err != nil {
			return SDKGateKit{}, err
		}
		seen[id] = true
		kit.Parameterized = append(kit.Parameterized, gate)
	}
	for id := range overlay.Gates {
		if !seen[id] {
			return SDKGateKit{}, fmt.Errorf("overlay gate %q is not in the engine gate kit — remove it from data/sdk.overlay.yaml", id)
		}
	}
	return kit, nil
}

func mergeUnitKinds(overlay *SDKOverlay) ([]SDKUnitKind, error) {
	roots := extpacks.UnitKindRoots()
	seen := make(map[string]bool, len(roots))
	out := make([]SDKUnitKind, 0, len(roots))
	for _, id := range roots {
		entry, ok := overlay.UnitKinds[id]
		if !ok || strings.TrimSpace(entry.Description) == "" {
			return nil, fmt.Errorf("unit kind %q has no description in data/sdk.overlay.yaml", id)
		}
		if strings.TrimSpace(entry.UnitIDForm) == "" {
			return nil, fmt.Errorf("unit kind %q has no unit_id_form in data/sdk.overlay.yaml", id)
		}
		scope := extpacks.ProjectScope(id)
		scopeToken := scope.String()
		if scopeToken == "" {
			return nil, fmt.Errorf("unit kind %q has no project-scope class in extpacks", id)
		}
		seen[id] = true
		out = append(out, SDKUnitKind{
			ID:           id,
			UnitIDForm:   entry.UnitIDForm,
			ProjectScope: scopeToken,
			Ownable:      !extpacks.ProviderScoped(id),
			Description:  entry.Description,
		})
	}
	for id := range overlay.UnitKinds {
		if !seen[id] {
			return nil, fmt.Errorf("overlay unit kind %q is not an engine kind root — remove it from data/sdk.overlay.yaml", id)
		}
	}
	return out, nil
}

func mergeStockPacks(moduleRoot string, overlay *SDKOverlay) ([]SDKPack, error) {
	stock, err := extpacks.DiscoverStockContent()
	if err != nil {
		return nil, fmt.Errorf("discover stock packs: %w", err)
	}
	seen := make(map[string]bool, len(stock))
	out := make([]SDKPack, 0, len(stock))
	for _, pack := range stock {
		id := pack.Manifest.ID
		entry, ok := overlay.Packs[id]
		if !ok || strings.TrimSpace(entry.Description) == "" {
			return nil, fmt.Errorf("stock pack %q has no description in data/sdk.overlay.yaml", id)
		}
		seen[id] = true
		out = append(out, SDKPack{
			ID:          id,
			Name:        pack.Manifest.Name,
			Feature:     pack.Manifest.Feature,
			Requires:    pack.Manifest.DependencyIDs(),
			Units:       len(pack.Units),
			Description: entry.Description,
		})
	}
	for id := range overlay.Packs {
		if !seen[id] {
			return nil, fmt.Errorf("overlay pack %q is not a stock pack — remove it from data/sdk.overlay.yaml", id)
		}
	}
	return out, nil
}

func mergeCoordinatorSurfaces(moduleRoot string, overlay *SDKOverlay) ([]SDKSurface, error) {
	surfaces, err := loadCoordinatorSurfaces(moduleRoot)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(surfaces))
	for i := range surfaces {
		id := surfaces[i].ID
		entry, ok := overlay.Surfaces[id]
		if !ok || strings.TrimSpace(entry.Description) == "" {
			return nil, fmt.Errorf("coordinator surface %q has no description in data/sdk.overlay.yaml", id)
		}
		seen[id] = true
		surfaces[i].Description = strings.TrimSpace(entry.Description)
	}
	for id := range overlay.Surfaces {
		if !seen[id] {
			return nil, fmt.Errorf("overlay coordinator surface %q is not an engine surface — remove it from data/sdk.overlay.yaml", id)
		}
	}
	return surfaces, nil
}

// loadTutorialPack projects the tutorial fixture pack's file bodies. The
// manifest sorts first because that is the order the page walks the reader
// through; the rest are lexical so a new file lands somewhere predictable.
// Conformance fixtures are included — the tutorial teaches writing them.
func loadTutorialPack(moduleRoot string) ([]SDKTutorialFile, error) {
	root := filepath.Join(moduleRoot, "config", "fixtures", "tutorial-pack")
	var rel []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		r, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = append(rel, filepath.ToSlash(r))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk tutorial pack: %w", err)
	}
	if len(rel) == 0 {
		return nil, fmt.Errorf("%s: tutorial pack has no files", root)
	}
	sort.Slice(rel, func(i, j int) bool {
		if (rel[i] == "extension.yaml") != (rel[j] == "extension.yaml") {
			return rel[i] == "extension.yaml"
		}
		return rel[i] < rel[j]
	})
	out := make([]SDKTutorialFile, 0, len(rel))
	for _, r := range rel {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(r))) // #nosec G304 -- bundled fixture path
		if err != nil {
			return nil, fmt.Errorf("read tutorial file %s: %w", r, err)
		}
		if len(strings.TrimSpace(string(body))) == 0 {
			return nil, fmt.Errorf("tutorial file %s is empty", r)
		}
		out = append(out, SDKTutorialFile{Path: r, Body: string(body)})
	}
	return out, nil
}

// mergeWorkflowManifestSDK projects the loader's reflected field inventory with
// one line of docs copy per field path.
func mergeWorkflowManifestSDK(out *WebsiteSDK, overlay *SDKOverlay) error {
	pathSet := map[string]bool{}
	for _, f := range workflowdef.ManifestFieldInventory() {
		pathSet[f.Path] = true
		entry, ok := overlay.WorkflowManifest[f.Path]
		if !ok || strings.TrimSpace(entry.Description) == "" {
			return fmt.Errorf("workflow manifest field %q has no description in data/sdk.overlay.yaml", f.Path)
		}
		out.WorkflowManifest = append(out.WorkflowManifest, SDKManifestField{
			Section:     f.Section,
			Key:         f.Key,
			Path:        f.Path,
			Type:        f.Type,
			Description: entry.Description,
		})
	}
	for path := range overlay.WorkflowManifest {
		if !pathSet[path] {
			return fmt.Errorf("overlay workflow manifest field %q is not a field the loader parses — remove it from data/sdk.overlay.yaml", path)
		}
	}
	return nil
}

// loadNativeToolCatalog joins the native tool manifest (ids and families) with
// the per-tool schema descriptions. A manifest id with no schema unit is an
// error. command is absent from the manifest and is not synthesized here.
func loadNativeToolCatalog(moduleRoot string) ([]SDKTool, error) {
	toolsDir := filepath.Join(moduleRoot, "config", "packs", "painted-wolf", "platform", "tools")

	manifestPath := filepath.Join(toolsDir, "native-tools.yaml")
	manifestData, err := os.ReadFile(manifestPath) // #nosec G304 -- bundled catalog path
	if err != nil {
		return nil, fmt.Errorf("read native tool manifest: %w", err)
	}
	var manifest struct {
		Native yaml.Node `yaml:"native"`
	}
	if err := yaml.Unmarshal(manifestData, &manifest); err != nil {
		return nil, fmt.Errorf("%s: %w", manifestPath, err)
	}
	if manifest.Native.Kind != yaml.MappingNode || len(manifest.Native.Content) == 0 {
		return nil, fmt.Errorf("%s: native must be a non-empty mapping", manifestPath)
	}

	// Tool copy lives in one unit per tool: tools/schemas/<name>.yaml.
	schemaDir := filepath.Join(toolsDir, "schemas")
	entries, err := os.ReadDir(schemaDir)
	if err != nil {
		return nil, fmt.Errorf("read tool schema units: %w", err)
	}
	descriptions := make(map[string]string, len(entries))
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(schemaDir, ent.Name())
		data, err := os.ReadFile(path) // #nosec G304 -- bundled catalog path
		if err != nil {
			return nil, fmt.Errorf("read tool schema unit: %w", err)
		}
		var unit struct {
			Description string `yaml:"description"`
		}
		if err := yaml.Unmarshal(data, &unit); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		descriptions[strings.TrimSuffix(ent.Name(), ".yaml")] = unit.Description
	}

	var out []SDKTool
	seen := map[string]string{}
	for i := 0; i+1 < len(manifest.Native.Content); i += 2 {
		family := strings.TrimSpace(manifest.Native.Content[i].Value)
		var ids []string
		if err := manifest.Native.Content[i+1].Decode(&ids); err != nil {
			return nil, fmt.Errorf("%s: family %q: %w", manifestPath, family, err)
		}
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id == "" {
				return nil, fmt.Errorf("%s: family %q has an empty tool id", manifestPath, family)
			}
			if prev, dup := seen[id]; dup {
				return nil, fmt.Errorf("%s: native tool %q is listed in both %q and %q", manifestPath, id, prev, family)
			}
			seen[id] = family
			desc := strings.TrimSpace(descriptions[id])
			if desc == "" {
				return nil, fmt.Errorf("native tool %q has no tools/schemas/%s.yaml description", id, id)
			}
			out = append(out, SDKTool{Name: id, Family: family, Description: desc})
		}
	}
	return out, nil
}

// anchorCatalogRow is the docs-facing subset of an anchor catalog entry; the
// envelope and dedup blocks are engine wiring the website does not render.
type anchorCatalogRow struct {
	ID           string   `yaml:"id"`
	Title        string   `yaml:"title"`
	Description  string   `yaml:"description"`
	TriggerClass string   `yaml:"trigger_class"`
	Surface      string   `yaml:"surface"`
	Planes       []string `yaml:"planes"`
}

// loadAnchorCatalog projects the host anchor catalog in file order — the same
// grouping the catalog author chose, which is the order the docs read best in.
// The catalog is decoded as plain YAML rather than through the coordinator
// packages so the website projection stays a leaf.
func loadAnchorCatalog(moduleRoot string) ([]SDKAnchor, error) {
	path := filepath.Join(moduleRoot, "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")
	data, err := os.ReadFile(path) // #nosec G304 -- bundled catalog path
	if err != nil {
		return nil, fmt.Errorf("read anchor catalog: %w", err)
	}
	var doc struct {
		Anchors []anchorCatalogRow `yaml:"anchors"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(doc.Anchors) == 0 {
		return nil, fmt.Errorf("%s: anchors must be a non-empty list", path)
	}
	var out []SDKAnchor
	seen := map[string]bool{}
	for _, row := range doc.Anchors {
		id := strings.TrimSpace(row.ID)
		if id == "" {
			return nil, fmt.Errorf("%s: anchor row has no id", path)
		}
		if seen[id] {
			return nil, fmt.Errorf("%s: anchor %q is declared twice", path, id)
		}
		seen[id] = true
		for field, value := range map[string]string{
			"title":         row.Title,
			"description":   row.Description,
			"trigger_class": row.TriggerClass,
			"surface":       row.Surface,
		} {
			if strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("%s: anchor %q has no %s", path, id, field)
			}
		}
		out = append(out, SDKAnchor{
			ID:           id,
			Title:        strings.TrimSpace(row.Title),
			Description:  strings.TrimSpace(row.Description),
			TriggerClass: strings.TrimSpace(row.TriggerClass),
			Surface:      strings.TrimSpace(row.Surface),
			Planes:       row.Planes,
		})
	}
	return out, nil
}

// loadCoordinatorSurfaces projects the surface catalog in declaration order —
// the routing-to-authoring order the file is written in. Descriptions are
// merged in by the caller from the website overlay: the catalog carries tool
// lists only.
func loadCoordinatorSurfaces(moduleRoot string) ([]SDKSurface, error) {
	path := filepath.Join(moduleRoot, "config", "packs", "painted-wolf", "platform", "host", "coordinator-surfaces.yaml")
	data, err := os.ReadFile(path) // #nosec G304 -- bundled catalog path
	if err != nil {
		return nil, fmt.Errorf("read coordinator surfaces: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: expected a top-level mapping of surfaces", path)
	}
	root := doc.Content[0]
	var out []SDKSurface
	seen := map[string]bool{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		id := strings.TrimSpace(root.Content[i].Value)
		if id == "" {
			return nil, fmt.Errorf("%s: surface entry has an empty id", path)
		}
		if seen[id] {
			return nil, fmt.Errorf("%s: surface %q is declared twice", path, id)
		}
		seen[id] = true
		var entry struct {
			Exit     string   `yaml:"exit"`
			Floor    []string `yaml:"floor"`
			Loadable []string `yaml:"loadable"`
		}
		if err := root.Content[i+1].Decode(&entry); err != nil {
			return nil, fmt.Errorf("%s: surface %q: %w", path, id, err)
		}
		if len(entry.Floor) == 0 {
			return nil, fmt.Errorf("%s: surface %q lists no floor tools", path, id)
		}
		// The SDK lists every tool the surface can offer: the floor on every
		// call plus what the turn may load.
		tools := append(append([]string(nil), entry.Floor...), entry.Loadable...)
		out = append(out, SDKSurface{ID: id, Exit: strings.TrimSpace(entry.Exit), Tools: tools})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no surfaces declared", path)
	}
	return out, nil
}

func mergeOARSDK(out *WebsiteSDK, overlay *SDKOverlay) error {
	factSet := map[string]bool{}
	for _, f := range oar.FactCatalogue() {
		factSet[f.Name] = true
		entry, ok := overlay.OARFacts[f.Name]
		if !ok || strings.TrimSpace(entry.Description) == "" {
			return fmt.Errorf("OAR fact %q has no description in data/sdk.overlay.yaml", f.Name)
		}
		// Render every tier; tier conveys portability.
		out.OAR.Facts = append(out.OAR.Facts, SDKFact{
			Name:          f.Name,
			Type:          f.Type,
			Class:         f.Class,
			Tier:          string(f.Tier),
			Profile:       f.Profile,
			PublishedName: f.PublishedName,
			Description:   entry.Description,
		})
	}
	for name := range overlay.OARFacts {
		if !factSet[name] {
			return fmt.Errorf("overlay OAR fact %q is not in the engine fact catalogue — remove it from data/sdk.overlay.yaml", name)
		}
	}

	fnSet := map[string]bool{}
	for _, f := range oar.ObservationFunctionCatalogue() {
		fnSet[f.Name] = true
		entry, ok := overlay.OARFunctions[f.Name]
		if !ok || strings.TrimSpace(entry.Description) == "" {
			return fmt.Errorf("OAR function %q has no description in data/sdk.overlay.yaml", f.Name)
		}
		out.OAR.Functions = append(out.OAR.Functions, SDKFactFunction{
			Name:          f.Name,
			Signature:     f.Signature,
			Tier:          string(f.Tier),
			Profile:       f.Profile,
			PublishedName: f.PublishedName,
			Description:   entry.Description,
		})
	}
	for name := range overlay.OARFunctions {
		if !fnSet[name] {
			return fmt.Errorf("overlay OAR function %q is not a registered observation function — remove it from data/sdk.overlay.yaml", name)
		}
	}
	return nil
}

func mergeExtensionDiagnosticsSDK(out *WebsiteSDK, overlay *SDKOverlay) error {
	extDiagSet := map[string]bool{}
	for _, code := range extpacks.AllDiagnosticCodes() {
		extDiagSet[code] = true
		entry, ok := overlay.ExtensionDiagnostics[code]
		if !ok || strings.TrimSpace(entry.Description) == "" {
			return fmt.Errorf("extension diagnostic %q has no description in data/sdk.overlay.yaml", code)
		}
		out.ExtensionDiagnostics = append(out.ExtensionDiagnostics, SDKDiagnostic{
			Code:        code,
			Description: entry.Description,
			Severity:    string(extpacks.SeverityForCode(code)),
		})
	}
	for code := range overlay.ExtensionDiagnostics {
		if !extDiagSet[code] {
			return fmt.Errorf("overlay extension diagnostic %q is not an engine code — remove it from data/sdk.overlay.yaml", code)
		}
	}
	return nil
}

func overlayGate(overlay *SDKOverlay, id string) (SDKGate, error) {
	entry, ok := overlay.Gates[id]
	if !ok || strings.TrimSpace(entry.Description) == "" {
		return SDKGate{}, fmt.Errorf("gate %q has no description in data/sdk.overlay.yaml", id)
	}
	return SDKGate{ID: id, Description: entry.Description}, nil
}

// loadPostureCatalog projects session-postures.yaml in declaration order, the
// lifecycle order (spec → build → orchestrate → vet). It decodes through the
// mapping node because a map[string] loses that order.
func loadPostureCatalog(moduleRoot string) ([]SDKPosture, error) {
	path := filepath.Join(moduleRoot, "config", "packs", "painted-wolf", "platform", "host", "session-postures.yaml")
	data, err := os.ReadFile(path) // #nosec G304 -- bundled catalog path
	if err != nil {
		return nil, fmt.Errorf("read session postures: %w", err)
	}
	var doc struct {
		Postures yaml.Node `yaml:"postures"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if doc.Postures.Kind != yaml.MappingNode || len(doc.Postures.Content) == 0 {
		return nil, fmt.Errorf("%s: postures must be a non-empty mapping", path)
	}
	var out []SDKPosture
	for i := 0; i+1 < len(doc.Postures.Content); i += 2 {
		id := strings.TrimSpace(doc.Postures.Content[i].Value)
		var entry postureEntryCopy
		if err := doc.Postures.Content[i+1].Decode(&entry); err != nil {
			return nil, fmt.Errorf("%s: posture %q: %w", path, id, err)
		}
		if strings.TrimSpace(entry.Description) == "" {
			return nil, fmt.Errorf("%s: posture %q has no description", path, id)
		}
		out = append(out, SDKPosture{
			ID:          id,
			Label:       strings.TrimSpace(entry.Label),
			Description: strings.TrimSpace(entry.Description),
		})
	}
	return out, nil
}

// postureEntryCopy is the docs-facing subset of a session-postures.yaml entry;
// the rules list is engine wiring the website does not render.
type postureEntryCopy struct {
	Description string `yaml:"description"`
	Label       string `yaml:"label"`
}

// loadWorkflowDiagCopy reads the human copy for every registered workflow
// diagnostic code from the platform pack catalog. The registry and the catalog
// are already bijection-checked in engine CI; missing copy fails here too so
// the website build cannot ship a bare code.
func loadWorkflowDiagCopy(moduleRoot string) ([]SDKDiagnostic, error) {
	dir := filepath.Join(moduleRoot, "config", "packs", "painted-wolf", "platform", "shared", "workflow-diagnostics")
	var out []SDKDiagnostic
	for _, code := range workflowdiag.AllCodes() {
		path := filepath.Join(dir, string(code)+".yaml")
		data, err := os.ReadFile(path) // #nosec G304 -- bundled catalog path
		if err != nil {
			return nil, fmt.Errorf("workflow diagnostic %q has no catalog copy: %w", code, err)
		}
		var doc workflowDiagCopy
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if doc.Code != string(code) {
			return nil, fmt.Errorf("%s: code %q does not match filename", path, doc.Code)
		}
		out = append(out, SDKDiagnostic{
			Code:        doc.Code,
			Message:     strings.TrimSpace(doc.Message),
			Replacement: strings.TrimSpace(doc.Replacement),
		})
	}
	return out, nil
}
