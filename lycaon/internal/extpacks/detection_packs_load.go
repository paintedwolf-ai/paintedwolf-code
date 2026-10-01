package extpacks

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/detectionpack"
)

// Detection units are additive, device-scoped, and bound to one provider.
const (
	// DetectionPackKindRoot is the pack-relative root carrying detection packs.
	DetectionPackKindRoot = "host/detection-packs"
	// DetectionPackUnitPrefix namespaces every detection unit id.
	DetectionPackUnitPrefix = DetectionPackKindRoot + "/"
	// detectionManifestLeaf is the manifest unit's last segment. A pack exists
	// exactly when this unit loads.
	detectionManifestLeaf = "pack"
	// detectionFixturesLeaf is the authoring corpus unit's last segment.
	detectionFixturesLeaf = "fixtures"
	// detectionRulesSegment is the directory segment holding rule documents.
	detectionRulesSegment = detectionpack.PackRulesDir
)

// detectionPackUnitID assigns provider-scoped ids to manifests, fixtures, and rules.
func detectionPackUnitID(provider string, parts []string) string {
	// parts[0:2] is the kind root itself.
	if len(parts) < 4 {
		return ""
	}
	provider = strings.TrimSpace(provider)
	packID := parts[2]
	if provider == "" || packID == "" || strings.HasPrefix(packID, ".") {
		return ""
	}
	base := detectionUnitBase(provider, packID)
	switch {
	case len(parts) == 4 && parts[3] == detectionpack.PackManifestFile:
		return base + detectionManifestLeaf
	case len(parts) == 4 && parts[3] == detectionpack.PackFixturesFile:
		return base + detectionFixturesLeaf
	case len(parts) == 5 && parts[3] == detectionRulesSegment:
		slug, ok := detectionpack.RuleFileSlug(parts[4])
		if !ok {
			return ""
		}
		return base + detectionRulesSegment + "/" + slug
	default:
		return ""
	}
}

// detectionUnitBase is the prefix every unit id in one provider's detection
// pack shares.
func detectionUnitBase(provider, packID string) string {
	return DetectionPackUnitPrefix + provider + ProviderSeparator + packID + "/"
}

// detectionUnitParts splits a detection unit id into its provider, pack, and leaf.
func detectionUnitParts(unitID string) (provider, packID, leaf string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(unitID), DetectionPackUnitPrefix)
	if !found {
		return "", "", "", false
	}
	provider, rest, ok = strings.Cut(rest, ProviderSeparator)
	if !ok || provider == "" {
		return "", "", "", false
	}
	packID, leaf, ok = strings.Cut(rest, "/")
	if !ok || packID == "" || leaf == "" {
		return "", "", "", false
	}
	return provider, packID, leaf, true
}

// LoadEffectiveDetectionPacks gives bundled packs first claim, then orders by provider and pack id.
// Diagnostics identify missing rules and refused duplicate claims.
func LoadEffectiveDetectionPacks(eff *EffectiveCatalog) ([]detectionpack.Pack, []Diagnostic) {
	if eff == nil {
		return nil, nil
	}
	groups := groupDetectionUnits(eff)
	sort.Slice(groups, func(i, j int) bool { return groups[i].less(groups[j], eff) })

	var out []detectionpack.Pack
	var diags []Diagnostic
	// The manifest pass establishes id claims before checking orphaned rules.
	claimed := map[string]*detectionGroup{}
	for _, g := range groups {
		if g.manifest == "" {
			continue
		}
		if winner, taken := claimed[g.packID]; taken {
			diags = append(diags, Diagnostic{
				Code:   DiagDetectionPackIDTaken,
				UnitID: g.manifest,
				PackID: g.provider,
				Message: fmt.Sprintf(
					"%s ships detection pack %q, which %s already provides; a detection pack id is claimed once, so %s keeps it and nothing %s ships under that name is used",
					g.provider, g.packID, winner.provider, winner.provider, g.provider),
			})
			continue
		}
		claimed[g.packID] = g
		pack, more := g.build(eff)
		diags = append(diags, more...)
		if pack != nil {
			out = append(out, *pack)
		}
	}
	for _, g := range groups {
		if g.manifest != "" || len(g.rules) == 0 {
			continue
		}
		if winner, taken := claimed[g.packID]; taken {
			// Provider-scoped unit ids alone cannot detect foreign rules under a claimed pack id.
			diags = append(diags, Diagnostic{
				Code:   DiagDetectionPackForeignUnit,
				UnitID: g.manifestUnitID(),
				PackID: g.provider,
				Message: fmt.Sprintf(
					"%s contributes %d rule(s) to detection pack %q, which %s provides; a pack may add its own detection packs, never rules to somebody else's",
					g.provider, len(g.rules), g.packID, winner.provider),
			})
			continue
		}
		diags = append(diags, Diagnostic{
			Code:   DiagDetectionPackInvalid,
			UnitID: g.manifestUnitID(),
			PackID: g.provider,
			Message: fmt.Sprintf("detection pack %q has no %s, so none of its rules load",
				g.packID, detectionpack.PackManifestFile),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if shipped(a) != shipped(b) {
			return shipped(a)
		}
		if a.ProviderPackID != b.ProviderPackID {
			return a.ProviderPackID < b.ProviderPackID
		}
		return a.ID < b.ID
	})
	return out, diags
}

// Bundled provenance gives shipped packs first claim on detection ids.
func shipped(p detectionpack.Pack) bool {
	return p.Source == detectionpack.SourceBundled || p.Source == detectionpack.SourceGenerated
}

// detectionGroup holds one provider's units for a detection pack.
type detectionGroup struct {
	provider string
	packID   string
	manifest string            // unit id, when present in the catalog at all
	rules    map[string]string // slug → unit id
}

// manifestUnitID names the group even when it shipped no manifest.
func (g *detectionGroup) manifestUnitID() string {
	if g.manifest != "" {
		return g.manifest
	}
	return detectionUnitBase(g.provider, g.packID) + detectionManifestLeaf
}

// less orders claims: shipped providers first, then by provider pack id, so a
// contested detection-pack id resolves by provenance rather than walk order.
func (g *detectionGroup) less(other *detectionGroup, eff *EffectiveCatalog) bool {
	gShipped, otherShipped := eff.StockAuthority(g.provider), eff.StockAuthority(other.provider)
	if gShipped != otherShipped {
		return gShipped
	}
	if g.provider != other.provider {
		return g.provider < other.provider
	}
	return g.packID < other.packID
}

func groupDetectionUnits(eff *EffectiveCatalog) []*detectionGroup {
	byKey := map[string]*detectionGroup{}
	for id := range eff.Units {
		provider, packID, leaf, ok := detectionUnitParts(id)
		if !ok {
			continue
		}
		key := detectionUnitBase(provider, packID)
		g := byKey[key]
		if g == nil {
			g = &detectionGroup{provider: provider, packID: packID, rules: map[string]string{}}
			byKey[key] = g
		}
		switch {
		case leaf == detectionManifestLeaf:
			g.manifest = id
		case strings.HasPrefix(leaf, detectionRulesSegment+"/"):
			g.rules[strings.TrimPrefix(leaf, detectionRulesSegment+"/")] = id
		}
	}
	groups := make([]*detectionGroup, 0, len(byKey))
	for _, g := range byKey {
		groups = append(groups, g)
	}
	return groups
}

// build validates one group and parses it into a pack. Its units share one
// provider by construction, so what is left is whether each survived resolve.
func (g *detectionGroup) build(eff *EffectiveCatalog) (*detectionpack.Pack, []Diagnostic) {
	var diags []Diagnostic
	manifest, provider, ok := loadedUnit(eff, g.manifest)
	if !ok {
		return nil, []Diagnostic{{
			Code:   DiagDetectionPackInvalid,
			UnitID: g.manifest,
			PackID: g.provider,
			Message: fmt.Sprintf("detection pack %q did not load: its manifest unit is %s",
				g.packID, unitStatusPhrase(eff, g.manifest)),
		}}
	}

	files := detectionpack.PackFiles{
		DirName:        g.packID,
		Manifest:       manifest,
		Source:         detectionSourceFor(eff, provider),
		ProviderPackID: provider,
		UnitID:         g.manifest,
	}

	slugs := make([]string, 0, len(g.rules))
	for slug := range g.rules {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		unitID := g.rules[slug]
		body, _, loaded := loadedUnit(eff, unitID)
		if !loaded {
			files.Warnings = append(files.Warnings, fmt.Sprintf(
				"rule %s is not active: its unit is %s", slug, unitStatusPhrase(eff, unitID)))
			continue
		}
		files.Rules = append(files.Rules, detectionpack.RuleFile{Name: slug + ".yml", Body: body})
	}

	pack, warnings := detectionpack.ParsePack(files)
	if pack == nil {
		return nil, append(diags, Diagnostic{
			Code:    DiagDetectionPackInvalid,
			UnitID:  g.manifest,
			PackID:  provider,
			Message: fmt.Sprintf("detection pack %q did not load: %s", g.packID, strings.Join(warnings, "; ")),
		})
	}
	return pack, diags
}

// detectionFixturesUnitID names the fixtures unit beside a manifest unit, so the
// corpus is read from the provider that won the id.
func detectionFixturesUnitID(manifestUnitID string) string {
	base, ok := strings.CutSuffix(strings.TrimSpace(manifestUnitID), "/"+detectionManifestLeaf)
	if !ok {
		return ""
	}
	return base + "/" + detectionFixturesLeaf
}

// RehearseDetectionPacks validates declared cases through production adapters.
// Rehearsal failures affect authoring validation, not catalog loading.
func RehearseDetectionPacks(eff *EffectiveCatalog, semantics *detectionpack.ActionSemanticsCatalog) []Diagnostic {
	if eff == nil {
		return nil
	}
	packs, _ := LoadEffectiveDetectionPacks(eff)
	var diags []Diagnostic
	for _, pack := range packs {
		unitID := detectionFixturesUnitID(pack.UnitID)
		body, _, ok := eff.UnitContent(unitID)
		if !ok {
			diags = append(diags, Diagnostic{
				Code: DiagDetectionRehearsalFailed, UnitID: unitID, PackID: pack.ProviderPackID,
				Message: fmt.Sprintf("detection pack %q ships no %s, so none of its rules are proven to match",
					pack.ID, detectionpack.PackFixturesFile),
			})
			continue
		}
		corpus, err := detectionpack.ParseFixtures(body)
		if err != nil {
			diags = append(diags, Diagnostic{
				Code: DiagDetectionRehearsalFailed, UnitID: unitID, PackID: pack.ProviderPackID,
				Message: fmt.Sprintf("detection pack %q: %v", pack.ID, err),
			})
			continue
		}
		for _, finding := range detectionpack.Rehearse(pack, corpus, semantics) {
			diags = append(diags, Diagnostic{
				Code: DiagDetectionRehearsalFailed, UnitID: unitID, PackID: pack.ProviderPackID,
				Message: finding.Message(),
			})
		}
	}
	return diags
}

// detectionSourceFor returns the provenance floor for a provider pack.
func detectionSourceFor(eff *EffectiveCatalog, providerPackID string) string {
	if eff.StockAuthority(providerPackID) {
		return detectionpack.SourceBundled
	}
	return detectionpack.SourceExtension
}

// loadedUnit returns a unit's winning bytes and provider when it is loaded.
func loadedUnit(eff *EffectiveCatalog, unitID string) (content []byte, packID string, ok bool) {
	if unitID == "" {
		return nil, "", false
	}
	return eff.UnitContent(unitID)
}

// unitStatusPhrase says why a unit is not loaded, in the resolver's own words.
func unitStatusPhrase(eff *EffectiveCatalog, unitID string) string {
	u, known := eff.Units[unitID]
	if !known {
		return "absent from the catalog"
	}
	switch u.Status {
	case UnitStatusDisabled:
		return "disabled in extension state"
	case UnitStatusConflict:
		return "contributed by more than one pack, so no version is used"
	default:
		return "not loaded (" + string(u.Status) + ")"
	}
}
