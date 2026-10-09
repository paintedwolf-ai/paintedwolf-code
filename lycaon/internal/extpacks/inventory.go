package extpacks

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
)

// inventoryKindRoots contains content resolved through pack composition.
var inventoryKindRoots = []string{
	"policy",
	"guidance",
	"workflows",
	ArchiveKindRoot,
	"agents",
	"tools",
	"approvals",
	"playbooks",
	"skills",
	"host/bindings",
	"host/detection-packs",
	CredentialSlotsKindRoot,
	"host/user-notices",
	"shared",
	"mcp_bindings",
	"contributions/commands",
	"contributions/menus",
	"contributions/keybindings",
	"contributions/editor-actions",
	"contributions/themes",
	"contributions/configuration",
	"contributions/mcp-requirements",
	"contributions/search-sources",
	"contributions/operations",
}

// ContributionRootPrefix namespaces the declarative contribution kind roots.
const ContributionRootPrefix = "contributions/"

// ProviderSeparator is excluded from pack ids, making scoped unit ids unambiguous.
const ProviderSeparator = ":"

// ProviderScoped identifies unit kinds namespaced by their providing pack.
func ProviderScoped(kindRoot string) bool {
	return strings.HasPrefix(kindRoot, ContributionRootPrefix) || kindRoot == DetectionPackKindRoot || kindRoot == CredentialSlotsKindRoot
}

// GuidanceGateFeedbackDir is shared by unit naming and feedback loading.
const GuidanceGateFeedbackDir = "gate-feedback"

// GateFeedbackUnitIDPrefix namespaces the gate-feedback provide units.
const GateFeedbackUnitIDPrefix = "guidance/" + GuidanceGateFeedbackDir + "/"

// ContributionKindRoots returns the contribution inventory roots in declaration order.
func ContributionKindRoots() []string {
	var out []string
	for _, root := range inventoryKindRoots {
		if strings.HasPrefix(root, ContributionRootPrefix) {
			out = append(out, root)
		}
	}
	return out
}

// ProjectScopeClass is how a kind root may participate when a project enables a pack.
type ProjectScopeClass uint8

const (
	ScopeShared ProjectScopeClass = iota + 1
	ScopeAdditive
	ScopeDeviceOnly
)

// String returns the stable wire / docs token for a project-scope class.
func (c ProjectScopeClass) String() string {
	switch c {
	case ScopeShared:
		return "shared"
	case ScopeAdditive:
		return "additive"
	case ScopeDeviceOnly:
		return "device_only"
	default:
		return ""
	}
}

// Every inventory root has one project-scope class.
var projectScopeByKind = map[string]ProjectScopeClass{
	"workflows": ScopeShared,
	// Sealed copies serve existing runs; projects cannot replace them.
	ArchiveKindRoot: ScopeDeviceOnly,
	"agents":        ScopeShared,
	"guidance":  ScopeShared,
	"shared":    ScopeShared,
	"policy":    ScopeAdditive,
	"approvals": ScopeAdditive,
	"playbooks": ScopeAdditive,
	// Contributions require device installation.
	"contributions/commands":         ScopeDeviceOnly,
	"contributions/menus":            ScopeDeviceOnly,
	"contributions/keybindings":      ScopeDeviceOnly,
	"contributions/editor-actions":   ScopeDeviceOnly,
	"contributions/configuration":    ScopeDeviceOnly,
	"contributions/mcp-requirements": ScopeDeviceOnly,
	"contributions/themes":           ScopeDeviceOnly,
	"contributions/search-sources":   ScopeDeviceOnly,
	"contributions/operations":       ScopeDeviceOnly,
	"tools":                          ScopeDeviceOnly,
	"host/bindings":                  ScopeDeviceOnly,
	"host/user-notices":              ScopeDeviceOnly,
	"mcp_bindings":                   ScopeDeviceOnly,
	"skills":                         ScopeDeviceOnly,
	// Recognition rules require device installation.
	DetectionPackKindRoot:   ScopeDeviceOnly,
	CredentialSlotsKindRoot: ScopeDeviceOnly,
}

// ProjectScope returns the project-scope class for a kind root, or 0 if unknown.
func ProjectScope(kindRoot string) ProjectScopeClass {
	return projectScopeByKind[kindRoot]
}

// ProjectDisableAllowed preserves device rules for additive kinds.
func ProjectDisableAllowed(kindRoot string, deviceResolved bool) bool {
	class := ProjectScope(kindRoot)
	return class == ScopeShared || (class == ScopeAdditive && !deviceResolved)
}

// kindRootForRel returns the longest matching inventory root.
func kindRootForRel(rel string) string {
	rel = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(rel)), "/")
	best := ""
	for _, root := range inventoryKindRoots {
		if rel != root && !strings.HasPrefix(rel, root+"/") {
			continue
		}
		if len(root) > len(best) {
			best = root
		}
	}
	return best
}

// KindRootForUnitID returns the matching inventory root, or empty for unknown roots.
func KindRootForUnitID(unitID string) string { return kindRootForRel(unitID) }

// UnitKindRoots returns the inventoried pack roots.
func UnitKindRoots() []string {
	return append([]string(nil), inventoryKindRoots...)
}

// Inventory roots distinguish unit ids from pack ids in disable lists.
func isUnitID(id string) bool {
	id = strings.TrimSpace(id)
	if !strings.Contains(id, "/") {
		return false
	}
	return kindRootForRel(id) != ""
}

// InventoryPack assigns stable unit ids across a pack tree.
func InventoryPack(p Pack, man Manifest) (PackContent, error) {
	pc := PackContent{
		Pack:     p,
		Manifest: man,
		Kind:     PackKindStock,
	}
	if err := validatePackYAMLConsumption(p); err != nil {
		return pc, err
	}
	var units []InventoriedUnit
	var diags []Diagnostic
	for _, kind := range inventoryKindRoots {
		dir := p.Root.Join(strings.Split(kind, "/")...)
		if !dir.IsDir() {
			continue
		}
		err := dir.Walk(func(at Source, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.IsDir() {
				base := info.Name()
				// Nested dependency trees do not define units.
				if base == ".git" || base == "vendor" || base == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			name := info.Name()
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return nil
			}
			rel := at.RelTo(p.Root)
			if rel == "" {
				return nil
			}
			id := UnitIDFor(p.ID, rel)
			if id == "" {
				return nil
			}
			if !info.Mode().IsRegular() {
				diags = append(diags, Diagnostic{
					Code:   DiagUnitNotRegular,
					UnitID: id,
					PackID: p.ID,
					Message: fmt.Sprintf("unit %s is not a regular file (%s); pack units must be regular files",
						id, info.Mode().Type()),
				})
				return nil
			}
			data, err := at.Read()
			if err != nil {
				return fmt.Errorf("read %s: %w", at, err)
			}
			if kind == "policy" {
				identity, _, oar, err := PolicyDocumentIdentity(data)
				if err != nil {
					return fmt.Errorf("%s: %w", at, err)
				}
				if oar {
					id = PolicyUnitID(identity)
				}
			}
			units = append(units, InventoriedUnit{
				ID:      id,
				Kind:    kind,
				Path:    at,
				Content: data,
			})
			return nil
		})
		if err != nil {
			return pc, err
		}
	}
	sort.Slice(units, func(i, j int) bool { return units[i].ID < units[j].ID })
	if err := validateUniqueUnitIDs(p.ID, units); err != nil {
		return pc, err
	}
	pc.Units = units
	pc.Diagnostics = diags
	return pc, nil
}

func validatePackYAMLConsumption(p Pack) error {
	return p.Root.Walk(func(at Source, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor", "node_modules":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		rel := at.RelTo(p.Root)
		if !isYAMLPath(rel) || packYAMLConsumed(p.ID, rel) {
			return nil
		}
		return fmt.Errorf("%s: YAML has no catalog unit or fixed-path consumer", rel)
	})
}

func isYAMLPath(rel string) bool {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	return strings.HasSuffix(rel, ".yaml") || strings.HasSuffix(rel, ".yml")
}

func packYAMLConsumed(packID, rel string) bool {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == config.PackManifestName || rel == filepath.ToSlash(filepath.Join("agents", "prompts", config.PersonaContractFile)) {
		return true
	}
	if strings.HasPrefix(filepath.Base(rel), "_") {
		return false
	}
	parts := strings.Split(rel, "/")
	if len(parts) == 2 && parts[0] == "profiles" && strings.HasSuffix(parts[1], ".yaml") {
		return true
	}
	if UnitIDFor(packID, rel) != "" {
		return true
	}
	leaf, stock := strings.CutPrefix(packID, StockPackIDPrefix)
	return stock && !strings.Contains(leaf, "/") && config.ConsumesPackYAML(config.StockPacks.Join(leaf, rel))
}

func validateUniqueUnitIDs(packID string, units []InventoriedUnit) error {
	seen := make(map[string]Source, len(units))
	for _, unit := range units {
		if previous, ok := seen[unit.ID]; ok {
			return fmt.Errorf("pack %s defines unit %s more than once (%s and %s)", packID, unit.ID, previous.String(), unit.Path.String())
		}
		seen[unit.ID] = unit.Path
	}
	return nil
}

// DiagUnitNotRegular identifies non-regular files excluded from publication.
const DiagUnitNotRegular = "unit_not_regular"

// UnitIDFor maps a pack-relative path to its public unit id.
// Provider-scoped kinds return empty when packID is missing.
func UnitIDFor(packID, rel string) string {
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "/")
	if rel == "" {
		return ""
	}
	parts := strings.Split(rel, "/")
	kindRoot := kindRootForRel(rel)
	if ProviderScoped(kindRoot) && strings.TrimSpace(packID) == "" {
		return ""
	}
	if strings.HasPrefix(kindRoot, ContributionRootPrefix) {
		// contributions/<kind>/<name>.yaml → contributions/<kind>/<pack-id>:<name>.
		// Each contribution is one flat YAML file.
		if len(parts) != 3 || !strings.HasSuffix(parts[2], ".yaml") {
			return ""
		}
		stem := strings.TrimSuffix(parts[2], ".yaml")
		if stem == "" {
			return ""
		}
		return kindRoot + "/" + packID + ProviderSeparator + stem
	}
	switch kindRoot {
	case "workflows":
		// workflows/<id>/workflow.yaml → workflows/<id>
		if len(parts) >= 2 && parts[len(parts)-1] == "workflow.yaml" {
			return "workflows/" + parts[1]
		}
		// workflows/_templates/name.yaml → workflows/_templates/name (same for _topologies)
		if len(parts) >= 3 && strings.HasPrefix(parts[1], "_") {
			leaf := parts[len(parts)-1]
			stem := strings.TrimSuffix(strings.TrimSuffix(leaf, ".yaml"), ".yml")
			if stem == "" || stem == leaf {
				return ""
			}
			return "workflows/" + parts[1] + "/" + stem
		}
		return ""
	case ArchiveKindRoot:
		return archiveUnitID(parts)
	case "skills":
		// Only SKILL.md defines a unit; adjacent files remain its payload.
		if len(parts) == 3 && parts[2] == "SKILL.md" {
			return "skills/" + parts[1]
		}
		return ""
	case "policy":
		if len(parts) != 2 {
			return ""
		}
		ext := filepath.Ext(parts[1])
		if ext != ".json" && ext != ".yaml" && ext != ".yml" {
			return ""
		}
		return "policy/" + strings.TrimSuffix(parts[1], ext)
	case "guidance":
		if strings.HasSuffix(parts[len(parts)-1], ".md") {
			// guidance/foo.md or guidance/enrich/foo.md
			return strings.TrimSuffix(rel, ".md")
		}
		// Gate feedback participates in the catalog revision; other guidance YAML is fixed.
		if len(parts) == 3 && parts[1] == GuidanceGateFeedbackDir && strings.HasSuffix(parts[2], ".yaml") {
			return strings.TrimSuffix(rel, ".yaml")
		}
		return ""
	case "host/bindings", "host/user-notices":
		if strings.HasSuffix(rel, ".yaml") || strings.HasSuffix(rel, ".yml") {
			return strings.TrimSuffix(strings.TrimSuffix(rel, ".yaml"), ".yml")
		}
		return ""
	case DetectionPackKindRoot:
		return detectionPackUnitID(packID, parts)
	case CredentialSlotsKindRoot:
		if len(parts) != 3 || !strings.HasSuffix(parts[2], ".yaml") {
			return ""
		}
		stem := strings.TrimSuffix(parts[2], ".yaml")
		if stem == "" || strings.HasPrefix(stem, ".") || strings.HasPrefix(stem, "_") || strings.Contains(stem, ProviderSeparator) {
			return ""
		}
		return kindRoot + "/" + packID + ProviderSeparator + stem
	case "agents":
		if strings.HasSuffix(rel, ".md") || strings.HasSuffix(rel, ".yaml") {
			return strings.TrimSuffix(strings.TrimSuffix(rel, ".md"), ".yaml")
		}
		return ""
	case "approvals", "playbooks", "tools":
		if strings.HasSuffix(rel, ".yaml") || strings.HasSuffix(rel, ".yml") || strings.HasSuffix(rel, ".md") {
			base := rel
			for _, ext := range []string{".yaml", ".yml", ".md"} {
				base = strings.TrimSuffix(base, ext)
			}
			return base
		}
		return ""
	case "shared":
		if strings.HasSuffix(rel, ".md") || strings.HasSuffix(rel, ".yaml") {
			base := rel
			for _, ext := range []string{".md", ".yaml"} {
				base = strings.TrimSuffix(base, ext)
			}
			return base
		}
		return ""
	case "mcp_bindings":
		// mcp_bindings/<id>.yaml → mcp_bindings/<id>.
		if len(parts) != 2 {
			return ""
		}
		leaf := parts[1]
		stem := strings.TrimSuffix(strings.TrimSuffix(leaf, ".yaml"), ".yml")
		if stem == "" || stem == leaf {
			return ""
		}
		return MCPBindingUnitID(stem)
	default:
		return ""
	}
}

// LoadManifest reads extension.yaml from a pack root on disk — an install
// staging dir, a cached pack, or a linked author folder.
func LoadManifest(packRoot string) (Manifest, error) { return ManifestAt(OnDisk(packRoot)) }

// ManifestAt reads extension.yaml from a pack root of either kind.
func ManifestAt(root Source) (Manifest, error) {
	data, err := root.Join(config.PackManifestName).Read()
	if err != nil {
		return Manifest{}, err
	}
	return ParseManifest(root.String(), data)
}

var (
	stockMu         sync.Mutex
	stockContentSet bool
	stockContentGen uint64
	stockContent    []PackContent
	stockContentErr error
)

// DiscoverStockContent inventories every stock pack once per bundled-source generation.
func DiscoverStockContent() ([]PackContent, error) {
	gen := config.SourceGeneration()
	stockMu.Lock()
	defer stockMu.Unlock()
	if !stockContentSet || stockContentGen != gen {
		stockContent, stockContentErr = inventoryStockContent()
		stockContentGen, stockContentSet = gen, true
	}
	if stockContentErr != nil {
		return nil, stockContentErr
	}
	return clonePackContents(stockContent), nil
}

func inventoryStockContent() ([]PackContent, error) {
	packs, err := discoverStockAll()
	if err != nil {
		return nil, err
	}
	out := make([]PackContent, 0, len(packs))
	for _, p := range packs {
		man, err := ManifestAt(p.Root)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.ID, err)
		}
		pc, err := InventoryPack(p, man)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.ID, err)
		}
		out = append(out, pc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pack.ID < out[j].Pack.ID })
	return out, nil
}

func clonePackContents(in []PackContent) []PackContent {
	out := make([]PackContent, len(in))
	for i, pc := range in {
		out[i] = pc
		out[i].Units = slices.Clone(pc.Units)
		out[i].Diagnostics = slices.Clone(pc.Diagnostics)
	}
	return out
}
