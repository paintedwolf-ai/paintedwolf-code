package detectionpack

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/config"
	"gopkg.in/yaml.v3"
)

// Where a pack's bytes came from. Provenance is presentation and policy: it
// decides collision order, what Settings may offer (a shipped pack is turned
// off, never removed), and what the person is told about who wrote a rule.
const (
	// SourceBundled ships in the binary, inside a stock extension pack.
	SourceBundled = "bundled"
	// SourceGenerated is bundled content a codegen target authored.
	SourceGenerated = "generated"
	// SourceExtension was contributed by an installed, non-stock extension pack.
	SourceExtension = "extension"
	// SourceDevice was imported as a folder into the device detection directory.
	SourceDevice = "device"
)

// Pack is one loaded detection pack. Rules includes unsupported rules so Settings
// can explain why they are inert.
type Pack struct {
	ID          string
	Label       string
	Description string
	Source      string // bundled | generated | extension | device
	// ProviderPackID names the extension pack whose tree carries this detection
	// pack. Empty for a device-imported folder, which belongs to no extension.
	ProviderPackID string
	// UnitID is the catalog unit id of the pack manifest, so a diagnostic about
	// a pack the person installed names the thing they can act on. Empty for a
	// device-imported folder.
	UnitID  string
	Enabled bool
	Rules   []Rule
	// Equivalents maps drop-in binary names for Settings "Also covers" copy.
	Equivalents map[string][]string
	// LoadWarnings records packs/rules that were rejected, for diagnostics.
	LoadWarnings []string
}

// Removable reports whether Settings may delete this pack outright. Only a
// folder the person imported is theirs to remove; everything else arrives with
// an extension pack and is uninstalled or turned off, never deleted piecemeal.
func (p Pack) Removable() bool { return p.Source == SourceDevice }

// Catalog is the merged contributed + device + project detection catalog.
type Catalog struct {
	Packs []Pack
	// Warnings describe what this device installed — a pack that would not parse,
	// a rules directory that would not read. They belong in the diagnostics bundle.
	Warnings []string
	// Rejected describes what the person wrote in this project's overlay and the
	// merge refused, for Settings to render.
	Rejected []RejectedRow
}

// Input is everything one catalog load merges.
//
// Contributed carries the packs the extension catalog resolved, already parsed
// and in precedence order (stock first, then installed packs by pack id). The
// merge never reads the extension catalog itself: resolve decides which units
// win, and this package decides what a winning unit means.
type Input struct {
	ConfigDir   string
	ProjectDir  string
	Contributed []Pack
}

type packManifest struct {
	ID          string              `yaml:"id"`
	Label       string              `yaml:"label"`
	Description string              `yaml:"description"`
	Source      string              `yaml:"source"`
	Equivalents map[string][]string `yaml:"equivalents"`
}

var packIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,39}$`)

const (
	// PackManifestFile is the pack's manifest, whose stem also names its unit.
	PackManifestFile = "pack.yaml"
	// PackRulesDir holds one Sigma document per file.
	PackRulesDir = "rules"
	// PackFixturesFile is the authoring corpus: positives that must match and
	// negatives that must not, rehearsed by validation rather than the matcher.
	PackFixturesFile = "fixtures.yaml"
)

// ruleExtensions are the spellings a rule document may use. Both are accepted
// wherever rules are read, so a file named the way the manifest beside it is
// named loads instead of silently not existing.
var ruleExtensions = []string{".yml", ".yaml"}

// RuleFileSlug returns the rule slug for a file under rules/, and false when the
// name is not a rule document. The slug is the unit id's last segment, the
// fixture corpus key, and what Settings shows beside a rule.
func RuleFileSlug(name string) (string, bool) {
	name = strings.TrimSpace(name)
	for _, ext := range ruleExtensions {
		if stem := strings.TrimSuffix(name, ext); stem != name && stem != "" {
			return stem, true
		}
	}
	return "", false
}

// RuleRelSlug is RuleFileSlug over a pack-relative path. Rules are flat: a
// nested path under rules/ is not a rule, so a directory of drafts beside them
// neither loads nor half-loads.
func RuleRelSlug(rel string) (string, bool) {
	name, found := strings.CutPrefix(filepath.ToSlash(strings.TrimSpace(rel)), PackRulesDir+"/")
	if !found || strings.Contains(name, "/") {
		return "", false
	}
	return RuleFileSlug(name)
}

// DevicePacksDir returns <configDir>/detection-packs.
func DevicePacksDir(configDir string) string {
	return filepath.Join(configDir, "detection-packs")
}

// DeviceStatePath returns <configDir>/detection-packs.yaml.
func DeviceStatePath(configDir string) string {
	return filepath.Join(configDir, "detection-packs.yaml")
}

// LoadCatalog merges the packs the extension catalog contributed with the device
// folder under ConfigDir, applies the disabled-pack state file and then the
// project's enable-only overlay, and never returns an error for bad content —
// unreadable packs become Warnings.
//
// ProjectDir is non-empty only when project scan configuration applies.
func LoadCatalog(in Input) (*Catalog, error) {
	cat := &Catalog{}
	claimed := map[string]Pack{}
	var packs []Pack

	// Contributed packs arrive in precedence order. A second pack claiming an id
	// is refused rather than merged or shadowed, so an installed pack cannot
	// replace a shipped one.
	for _, p := range in.Contributed {
		if incumbent, clash := claimed[p.ID]; clash {
			cat.Warnings = append(cat.Warnings, collisionWarning(p, incumbent))
			continue
		}
		claimed[p.ID] = p
		packs = append(packs, p)
		cat.Warnings = append(cat.Warnings, p.LoadWarnings...)
	}

	if in.ConfigDir != "" {
		deviceRoot := DevicePacksDir(in.ConfigDir)
		if deviceEnts, err := os.ReadDir(deviceRoot); err == nil {
			for _, ent := range deviceEnts {
				if !ent.IsDir() || strings.HasPrefix(ent.Name(), ".") {
					continue
				}
				p, warnings := ParsePack(devicePackFiles(filepath.Join(deviceRoot, ent.Name())))
				cat.Warnings = append(cat.Warnings, warnings...)
				if p == nil {
					continue
				}
				if incumbent, clash := claimed[p.ID]; clash {
					cat.Warnings = append(cat.Warnings, collisionWarning(*p, incumbent))
					continue
				}
				claimed[p.ID] = *p
				packs = append(packs, *p)
			}
		} else if !os.IsNotExist(err) {
			cat.Warnings = append(cat.Warnings, fmt.Sprintf("read device packs: %v", err))
		}
	}

	disabled, err := loadDisabledIDs(in.ConfigDir)
	if err != nil {
		cat.Warnings = append(cat.Warnings, fmt.Sprintf("read detection-packs.yaml: %v", err))
	}
	for i := range packs {
		if disabled[packs[i].ID] {
			packs[i].Enabled = false
		} else {
			packs[i].Enabled = true
		}
	}

	// The project tier lands after the device state so a repository's answer wins
	// for work in that tree.
	if strings.TrimSpace(in.ProjectDir) != "" {
		rows, rejected := loadProjectPackRows(in.ProjectDir)
		cat.Rejected = append(cat.Rejected, rejected...)
		cat.Rejected = append(cat.Rejected, applyProjectEnableOverlay(packs, rows)...)
	}

	// Contested rule ids resolve in merge order, before the list is sorted for
	// display: precedence is who arrived first, not whose pack id sorts lower.
	cat.Warnings = append(cat.Warnings, markDuplicateRuleIDs(packs)...)
	sort.Slice(packs, func(i, j int) bool { return packs[i].ID < packs[j].ID })
	cat.Packs = packs
	return cat, nil
}

// PackFiles is one pack's bytes, already gathered from wherever that pack lives:
// a stock pack out of the shipped config value, an installed pack out of its
// resolved catalog units, a device pack off disk. Gathering is the only place
// that distinction appears; validation below is shared.
type PackFiles struct {
	// DirName is the directory the pack occupies, which its manifest id must equal.
	DirName string
	// Manifest is pack.yaml. Nil means the pack did not load at all.
	Manifest []byte
	Rules    []RuleFile
	// Source is the provenance floor for this gather — a manifest may narrow it
	// (bundled → generated) but never claim a stronger one.
	Source string
	// ProviderPackID and UnitID name the extension pack and manifest unit that
	// carry this pack. Both empty for a device-imported folder.
	ProviderPackID string
	UnitID         string
	// Warnings records what the gather could not read.
	Warnings []string
}

// RuleFile is one rules/<name> document.
type RuleFile struct {
	// Name is the file name including its .yml extension.
	Name string
	Body []byte
}

// maxRulesPerPack bounds one pack's rule files, whatever gathered it, so a
// runaway directory cannot swell the matcher. A pack past the cap loads its
// first rules and says how many it dropped.
const maxRulesPerPack = 200

// maxRuleBytes bounds one rule document.
const maxRuleBytes = 64 * 1024

// ShippedPacks gathers detection packs from the stock pack tree without consulting the catalog.
func ShippedPacks() ([]Pack, []string, error) {
	ents, err := config.List(config.DetectionPacksDir)
	if err != nil {
		return nil, nil, fmt.Errorf("list shipped detection packs: %w", err)
	}
	var packs []Pack
	var warnings []string
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		p, packWarnings := ParsePack(bundledPackFiles(config.DetectionPacksDir.Join(ent.Name())))
		warnings = append(warnings, packWarnings...)
		if p == nil {
			continue
		}
		packs = append(packs, *p)
	}
	sort.Slice(packs, func(i, j int) bool { return packs[i].ID < packs[j].ID })
	return packs, warnings, nil
}

// bundledPackFiles reads one shipped pack out of bundled config. The runtime
// catalog reaches shipped packs through their resolved catalog units; this
// gather serves ShippedPacks, the floor readers, and rehearsal.
func bundledPackFiles(dir config.Rel) PackFiles {
	files := PackFiles{DirName: dir.Base(), Source: SourceBundled}
	data, err := config.Read(dir.Join(PackManifestFile))
	if err != nil {
		files.Warnings = []string{fmt.Sprintf("pack %s: read pack.yaml: %v", files.DirName, err)}
		return files
	}
	files.Manifest = data

	rulesDir := dir.Join(PackRulesDir)
	ents, err := config.List(rulesDir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			files.Warnings = append(files.Warnings, fmt.Sprintf("pack %s: read rules: %v", files.DirName, err))
		}
		return files
	}
	for _, ent := range ents {
		name := ent.Name()
		if _, ok := RuleFileSlug(name); ent.IsDir() || !ok {
			continue
		}
		body, readErr := config.Read(rulesDir.Join(name))
		if readErr != nil {
			files.Warnings = append(files.Warnings, fmt.Sprintf("pack %s rule %s: %v", files.DirName, name, readErr))
			continue
		}
		files.Rules = append(files.Rules, RuleFile{Name: name, Body: body})
	}
	return files
}

// devicePackFiles reads one user-imported pack from the config directory.
func devicePackFiles(dir string) PackFiles {
	files := PackFiles{DirName: filepath.Base(dir), Source: SourceDevice}
	data, err := os.ReadFile(filepath.Join(dir, PackManifestFile)) // #nosec G304 -- device pack path under configDir
	if err != nil {
		files.Warnings = []string{fmt.Sprintf("pack %s: read pack.yaml: %v", files.DirName, err)}
		return files
	}
	files.Manifest = data

	rulesDir := filepath.Join(dir, PackRulesDir)
	ents, err := os.ReadDir(rulesDir)
	if err != nil {
		if !os.IsNotExist(err) {
			files.Warnings = append(files.Warnings, fmt.Sprintf("pack %s: read rules: %v", files.DirName, err))
		}
		return files
	}
	for _, ent := range ents {
		name := ent.Name()
		if _, ok := RuleFileSlug(name); ent.IsDir() || !ok {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(rulesDir, name)) // #nosec G304 -- device pack path under configDir
		if readErr != nil {
			files.Warnings = append(files.Warnings, fmt.Sprintf("pack %s rule %s: %v", files.DirName, name, readErr))
			continue
		}
		files.Rules = append(files.Rules, RuleFile{Name: name, Body: body})
	}
	return files
}

// ParsePack validates one gathered pack. A nil pack means nothing loaded and the
// warnings say why; a non-nil pack may still carry warnings about rules it
// dropped. Rule order follows the gather, which every gatherer sorts by name.
func ParsePack(files PackFiles) (*Pack, []string) {
	dirName := files.DirName
	if files.Manifest == nil {
		return nil, files.Warnings
	}
	warnings := files.Warnings
	var man packManifest
	if err := yaml.Unmarshal(files.Manifest, &man); err != nil {
		return nil, []string{fmt.Sprintf("pack %s: parse pack.yaml: %v", dirName, err)}
	}
	if man.ID == "" || man.ID != dirName {
		return nil, []string{fmt.Sprintf("pack %s: id %q must equal directory name", dirName, man.ID)}
	}
	if !packIDPattern.MatchString(man.ID) {
		return nil, []string{fmt.Sprintf("pack %s: invalid id", dirName)}
	}
	if strings.TrimSpace(man.Label) == "" || strings.TrimSpace(man.Description) == "" {
		return nil, []string{fmt.Sprintf("pack %s: label and description required", dirName)}
	}

	src, srcWarns := resolveProvenance(files.Source, man)
	warnings = append(warnings, srcWarns...)

	equivalents, eqWarns := sanitizeEquivalents(man.Equivalents)
	warnings = append(warnings, eqWarns...)

	ruleFiles := files.Rules
	if len(ruleFiles) > maxRulesPerPack {
		warnings = append(warnings, fmt.Sprintf(
			"pack %s: %d rule files exceeds the %d cap; the rest were not loaded",
			man.ID, len(ruleFiles), maxRulesPerPack))
		ruleFiles = ruleFiles[:maxRulesPerPack]
	}

	var rules []Rule
	for _, file := range ruleFiles {
		if len(file.Body) > maxRuleBytes {
			warnings = append(warnings, fmt.Sprintf(
				"pack %s rule %s: %d bytes exceeds the %d cap", man.ID, file.Name, len(file.Body), maxRuleBytes))
			continue
		}
		rule, err := ParseRule(file.Body)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("pack %s rule %s: %v", man.ID, file.Name, err))
			continue
		}
		rule.Slug, _ = RuleFileSlug(file.Name)
		rules = append(rules, rule)
	}

	return &Pack{
		ID:             man.ID,
		Label:          man.Label,
		Description:    man.Description,
		Source:         src,
		ProviderPackID: files.ProviderPackID,
		UnitID:         files.UnitID,
		Enabled:        true,
		Rules:          rules,
		Equivalents:    equivalents,
		LoadWarnings:   warnings,
	}, warnings
}

// resolveProvenance reconciles the gather's floor with the manifest's claim. A
// manifest may only narrow within what its gather already proved: shipped
// content marks itself generated, and everything else that claims a stronger
// provenance than its location earns is told so and held at the floor.
func resolveProvenance(floor string, man packManifest) (string, []string) {
	claim := strings.TrimSpace(man.Source)
	if claim == "" || claim == floor {
		return floor, nil
	}
	if floor == SourceBundled && claim == SourceGenerated {
		return SourceGenerated, nil
	}
	return floor, []string{fmt.Sprintf(
		"pack %q claimed source %s; it loaded as %s and is treated that way", man.ID, claim, floor)}
}

// collisionWarning explains a refused pack in terms of who provides each side,
// because "aws-cli collides" is unactionable without both names.
func collisionWarning(refused, incumbent Pack) string {
	return fmt.Sprintf("detection pack %q from %s was not loaded: %s already provides that id",
		refused.ID, packOriginLabel(refused), packOriginLabel(incumbent))
}

func packOriginLabel(p Pack) string {
	switch {
	case p.ProviderPackID != "":
		return "extension pack " + p.ProviderPackID
	case p.Source == SourceDevice:
		return "the device detection folder"
	default:
		return "a pack that ships with the app"
	}
}

func sanitizeEquivalents(in map[string][]string) (map[string][]string, []string) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string][]string, len(in))
	var warnings []string
	for canon, alts := range in {
		canon = strings.TrimSpace(strings.ToLower(canon))
		if canon == "" {
			warnings = append(warnings, "equivalents: empty canonical name dropped")
			continue
		}
		var clean []string
		for _, a := range alts {
			a = strings.TrimSpace(strings.ToLower(a))
			if a == "" {
				warnings = append(warnings, fmt.Sprintf("equivalents[%s]: empty alternative dropped", canon))
				continue
			}
			clean = append(clean, a)
		}
		if len(clean) == 0 {
			warnings = append(warnings, fmt.Sprintf("equivalents[%s]: no alternatives; dropped", canon))
			continue
		}
		out[canon] = clean
	}
	if len(out) == 0 {
		return nil, warnings
	}
	return out, warnings
}

// markDuplicateRuleIDs resolves rules that claim the same Sigma id; packs arrive
// in merge precedence order. Across packs the first claim wins and later ones go
// inert, so a pack cannot silence another's rule by shipping a stub with its id.
// Within one pack both definitions go inert.
func markDuplicateRuleIDs(packs []Pack) []string {
	// One claim on a rule id: which pack made it, and the rule itself, held by
	// pointer so the decision below writes straight to the loaded rule.
	type claim struct {
		packID string
		rule   *Rule
	}
	groups := map[string][]claim{}
	var ids []string
	for pi := range packs {
		pack := &packs[pi]
		for ri := range pack.Rules {
			rule := &pack.Rules[ri]
			if _, seen := groups[rule.ID]; !seen {
				ids = append(ids, rule.ID)
			}
			groups[rule.ID] = append(groups[rule.ID], claim{packID: pack.ID, rule: rule})
		}
	}
	sort.Strings(ids)

	var warnings []string
	for _, id := range ids {
		claims := groups[id]
		if len(claims) < 2 {
			continue
		}
		incumbent := claims[0]
		samePack := true
		for _, later := range claims[1:] {
			if later.packID != incumbent.packID {
				samePack = false
				break
			}
		}
		contested := claims[1:]
		if samePack {
			contested = claims
		}
		for _, later := range contested {
			later.rule.Supported = false
			if samePack {
				later.rule.UnsupportedReason = "duplicate rule id inside this pack; every definition is inactive"
				warnings = append(warnings, fmt.Sprintf(
					"pack %s rule %s: duplicate rule id %s inside this pack; every definition is inactive",
					later.packID, later.rule.Slug, id))
				continue
			}
			later.rule.UnsupportedReason = fmt.Sprintf(
				"rule id already used by %s in %s, which keeps it", incumbent.rule.Slug, incumbent.packID)
			warnings = append(warnings, fmt.Sprintf(
				"pack %s rule %s: rule id %s is already used by %s/%s, which keeps it",
				later.packID, later.rule.Slug, id, incumbent.packID, incumbent.rule.Slug))
		}
	}
	return warnings
}

// SupportedFields returns the closed field vocabulary for one synthetic source.
// The authoring schema contract compares its projection to this runtime list.
func SupportedFields(source LogSource) []string {
	fields := fieldsForSource(source)
	out := make([]string, 0, len(fields))
	for field := range fields {
		out = append(out, field)
	}
	sort.Strings(out)
	return out
}

// PackByID returns the pack with the given id, or false.
func (c *Catalog) PackByID(id string) (Pack, bool) {
	if c == nil {
		return Pack{}, false
	}
	for _, p := range c.Packs {
		if p.ID == id {
			return p, true
		}
	}
	return Pack{}, false
}
