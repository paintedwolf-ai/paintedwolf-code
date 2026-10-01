package extpacks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/config"
)

// ResolveInput is the pure Resolve argument set. Pack list order is ignored.
type ResolveInput struct {
	Packs   []PackContent
	Desired DesiredState
	// An empty ProjectID selects the device catalog.
	ProjectID string
	// Nil scanner state does not satisfy requirements.
	Scanners ScannerRequirementChecker
	// Zero provenance assigns every key to device scope.
	Provenance DesiredProvenance
	// Omitted maps excluded pack IDs to compile failures.
	Omitted map[string]string
}

// EffectiveCatalog is the post-resolve unit set + diagnostics + inspect contributions.
type EffectiveCatalog struct {
	Units       map[string]UnitEffective // status for every known unit id
	Loaded      map[string]UnitEffective // status loaded|owned only (session engines)
	Diagnostics []Diagnostic
	Packs       []PackSummary
	Desired     DesiredState
	// Revision hashes resolved content, configuration, package integrity, and compiler version.
	Revision      string
	resolvedPacks map[string]Pack
	input         *ResolveInput // retained so ForCommitted can re-resolve
}

type packGate struct {
	enabled      bool
	apiOK        bool
	pathOK       bool
	scannersOK   bool
	scannersMiss []string
	omitted      bool
	omitReason   BlockedReason
	omitDetail   string
}

// Resolve computes effective = provide(enabled) − disabled ⊕ own.
// Pack slice order is not an input; contributions are sorted by pack_id for stable diagnostics.
func Resolve(ctx context.Context, in ResolveInput) *EffectiveCatalog {
	desired := in.Desired
	if desired.Own == nil {
		desired.Own = map[string]string{}
	}
	packs := sortedPackContents(in.Packs)
	gates := computePackGates(ctx, packs, desired, in)
	contributing := contributingFixpoint(packs, gates)
	summaries, diags := summarizePacks(packs, desired, gates, contributing)
	contributions, kindByID := collectContributions(packs, contributing)
	desired, ownDiags := refuseUnownable(desired, kindByID)
	diags = append(diags, ownDiags...)
	desired, contributions, floorDiags := applyProjectFloor(desired, in.Provenance, contributions, kindByID)
	diags = append(diags, floorDiags...)
	disabledSet := disabledSetFrom(desired)
	units, loaded, unitDiags := resolveUnits(contributions, kindByID, disabledSet, desired)
	diags = append(diags, unitDiags...)
	if !contributing[PlatformPackID] {
		diags = append(diags, Diagnostic{
			Code: DiagPlatformMissing, PackID: PlatformPackID,
			Message: PlatformPackID + " is not contributing; session boot will fail closed",
		})
	}
	eff := &EffectiveCatalog{
		Units:       units,
		Loaded:      loaded,
		Diagnostics: diags,
		Packs:       summaries,
		Desired:     desired,
	}
	eff.resolvedPacks = map[string]Pack{}
	for _, content := range packs {
		eff.resolvedPacks[content.Pack.ID] = content.Pack
	}
	_, approvalRuleDiags, _ := LoadEffectiveApprovalRules(eff)
	eff.Diagnostics = append(eff.Diagnostics, approvalRuleDiags...)
	// Schema and binding refusals belong on the catalog: the view build fails closed on them.
	_, schemaDiags, _ := LoadEffectiveToolSchemas(eff)
	eff.Diagnostics = append(eff.Diagnostics, schemaDiags...)
	_, bindingDiags, _ := LoadEffectiveBindings(eff)
	eff.Diagnostics = append(eff.Diagnostics, bindingDiags...)
	_, credentialDiags, _ := LoadEffectiveCredentialSlots(eff)
	eff.Diagnostics = append(eff.Diagnostics, credentialDiags...)
	// A skill that does not load, or will not render, is shipped and not running.
	_, skillDiags := LoadEffectiveSkills(eff)
	eff.Diagnostics = append(eff.Diagnostics, skillDiags...)
	// A detection-pack refusal belongs on the catalog; absence is a missed approval.
	_, detectionDiags := LoadEffectiveDetectionPacks(eff)
	eff.Diagnostics = append(eff.Diagnostics, detectionDiags...)
	eff.Diagnostics = StampSeverities(eff.Diagnostics)
	retained := in
	retained.Packs = packs
	eff.input = &retained
	eff.Revision = revisionOf(eff, packs)
	return eff
}

// WithOmitted re-resolves with additional packs held out, merged with any
// already omitted. Returns false when the catalog has no retained resolve input.
func (e *EffectiveCatalog) WithOmitted(ctx context.Context, reasons map[string]string) (*EffectiveCatalog, bool) {
	if e == nil || e.input == nil || len(reasons) == 0 {
		return nil, false
	}
	in := *e.input
	merged := make(map[string]string, len(in.Omitted)+len(reasons))
	for id, why := range in.Omitted {
		merged[id] = why
	}
	for id, why := range reasons {
		merged[id] = why
	}
	in.Omitted = merged
	return Resolve(ctx, in), true
}

func disabledSetFrom(desired DesiredState) map[string]struct{} {
	out := map[string]struct{}{}
	for _, id := range desired.Disabled {
		id = strings.TrimSpace(id)
		if id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

func sortedPackContents(in []PackContent) []PackContent {
	packs := append([]PackContent(nil), in...)
	sort.Slice(packs, func(i, j int) bool { return packs[i].Pack.ID < packs[j].Pack.ID })
	return packs
}

// Explicit pack rows control enablement; dependencies without rows follow their dependents.
func packEnablement(packs []PackContent, desired DesiredState) map[string]bool {
	enabled := make(map[string]bool, len(packs))
	derived := make([]PackContent, 0, len(packs))
	for _, pc := range packs {
		id := pc.Pack.ID
		if _, stated := DesiredPackRow(desired, id); stated || IsStockPackID(id) {
			enabled[id] = PackEnabled(desired, id)
			continue
		}
		derived = append(derived, pc)
	}
	if len(derived) == 0 {
		return enabled
	}
	needs := map[string][]string{} // dependency id → packages requiring it
	for _, pc := range packs {
		for dependencyID := range pc.Manifest.Dependencies {
			needs[dependencyID] = append(needs[dependencyID], pc.Pack.ID)
		}
	}
	// Fixpoint so a dependency of a dependency is reached. Each package enables
	// at most once, so the loop terminates.
	for {
		changed := false
		for _, pc := range derived {
			id := pc.Pack.ID
			if enabled[id] {
				continue
			}
			for _, requiring := range needs[id] {
				if enabled[requiring] {
					enabled[id] = true
					changed = true
					break
				}
			}
		}
		if !changed {
			return enabled
		}
	}
}

func computePackGates(
	ctx context.Context,
	packs []PackContent,
	desired DesiredState,
	in ResolveInput,
) map[string]packGate {
	projectID := in.ProjectID
	scanners := in.Scanners
	gates := map[string]packGate{}
	enabled := packEnablement(packs, desired)
	for _, pc := range packs {
		g := packGate{
			enabled:    enabled[pc.Pack.ID],
			apiOK:      ManifestHostCompatible(pc.Manifest),
			pathOK:     true,
			scannersOK: true,
		}
		if pc.OmitReason != "" {
			g.omitted = true
			g.omitReason = pc.OmitReason
			g.omitDetail = pc.OmitDetail
		}
		if reason, held := in.Omitted[pc.Pack.ID]; held {
			g.omitted = true
			if g.omitReason == "" {
				g.omitReason = BlockedInvalid
				g.omitDetail = reason
			}
		}
		if pc.Kind == PackKindPath {
			if _, err := pc.Pack.Root.Join(config.PackManifestName).Stat(); err != nil {
				g.pathOK = false
				g.apiOK = true // missing path uses the path diagnostic, not compatibility
			}
		}
		for _, scannerID := range pc.Manifest.RequiresScanners {
			scannerID = strings.TrimSpace(scannerID)
			if scannerID == "" {
				continue
			}
			met := scanners != nil && scanners.ScannerEnabled(ctx, projectID, scannerID)
			if !met {
				g.scannersOK = false
				g.scannersMiss = append(g.scannersMiss, scannerID)
			}
		}
		gates[pc.Pack.ID] = g
	}
	return gates
}

func contributingFixpoint(packs []PackContent, gates map[string]packGate) map[string]bool {
	contributing := map[string]bool{}
	versions := map[string]string{}
	for _, pc := range packs {
		versions[pc.Manifest.ID] = pc.Manifest.Version
	}
	for {
		changed := false
		for _, pc := range packs {
			id := pc.Pack.ID
			if contributing[id] {
				continue
			}
			g := gates[id]
			if !g.enabled || !g.apiOK || !g.pathOK || !g.scannersOK || g.omitted {
				continue
			}
			ok := true
			for _, req := range pc.Manifest.DependencyIDs() {
				req = strings.TrimSpace(req)
				if req == "" {
					continue
				}
				if !contributing[req] || !dependencyVersionSatisfied(pc.Manifest.Dependencies[req], versions[req]) {
					ok = false
					break
				}
			}
			if ok {
				contributing[id] = true
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return contributing
}

func summarizePacks(
	packs []PackContent,
	desired DesiredState,
	gates map[string]packGate,
	contributing map[string]bool,
) ([]PackSummary, []Diagnostic) {
	var diags []Diagnostic
	summaries := make([]PackSummary, 0, len(packs))
	dependencyOf := map[string][]string{}
	versions := map[string]string{}
	for _, pc := range packs {
		versions[pc.Manifest.ID] = pc.Manifest.Version
		for dependencyID := range pc.Manifest.Dependencies {
			dependencyOf[dependencyID] = append(dependencyOf[dependencyID], pc.Manifest.ID)
		}
	}
	for _, pc := range packs {
		id := pc.Pack.ID
		g := gates[id]
		sum := PackSummary{
			ID:           id,
			Name:         strings.TrimSpace(pc.Manifest.Name),
			Version:      pc.Manifest.Version,
			ExtensionAPI: pc.Manifest.Compatibility.ExtensionAPI,
			DependencyOf: append([]string(nil), dependencyOf[id]...),
			Kind:         pc.Kind,
			Bundled:      pc.Pack.Root.IsBundled(),
			UnitCount:    len(pc.Units),
			Removable:    false,
			Enabled:      g.enabled,
			Contributing: contributing[id],
			Feature:      strings.TrimSpace(pc.Manifest.Feature),
		}
		sort.Strings(sum.DependencyOf)
		sum.Dependencies = map[string]string{}
		for dependencyID, request := range pc.Manifest.Dependencies {
			sum.Dependencies[dependencyID] = request.Version
		}
		switch pc.Kind {
		case PackKindStock:
			sum.InstallationState = "stock"
			sum.InstallationScope = "stock"
		case PackKindPath:
			sum.InstallationState = "development"
			sum.InstallationScope = "transitive"
		default:
			sum.InstallationState = "release"
			sum.InstallationScope = "transitive"
		}
		if pc.Locked != nil {
			sum.ResolvedRevision = pc.Locked.Revision
			sum.Integrity = pc.Locked.Integrity
			sum.Dependencies = make(map[string]string, len(pc.Locked.Dependencies))
			for dependencyID, version := range pc.Locked.Dependencies {
				sum.Dependencies[dependencyID] = version
			}
		}
		if sum.Name == "" {
			sum.Name = id
		}
		if row, ok := DesiredPackRow(desired, id); ok {
			sum.Source = row.Source
			sum.Ref = row.Ref
			sum.VersionConstraint = row.Version
			if row.Ref != "" {
				sum.InstallationState = "pinned"
			}
			if strings.TrimSpace(row.Source) != "" && pc.Kind != PackKindStock {
				sum.InstallationScope = "device"
				sum.Removable = true
			}
		}
		if pc.Pack.Root.Join("profiles").IsDir() {
			sum.HasProfile = true
		}
		// Keep discovery refusals visible in the catalog.
		diags = append(diags, pc.Diagnostics...)
		sum, more := annotatePackBlock(sum, pc, g, contributing, versions)
		diags = append(diags, more...)
		summaries = append(summaries, sum)
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].ID < summaries[j].ID })
	return summaries, diags
}

func annotatePackBlock(
	sum PackSummary,
	pc PackContent,
	g packGate,
	contributing map[string]bool,
	versions map[string]string,
) (PackSummary, []Diagnostic) {
	id := pc.Pack.ID
	var diags []Diagnostic
	switch {
	case !g.pathOK:
		diags = append(diags, Diagnostic{
			Code: DiagPathMissing, PackID: id,
			Message: fmt.Sprintf("linked pack %s source is missing or unreadable; use Reload after restoring the folder", id),
		})
		sum.Contributing = false
	case g.omitted:
		sum.BlockedReason = g.omitReason
		if sum.BlockedReason == "" {
			sum.BlockedReason = BlockedInvalid
		}
		sum.Contributing = false
		code := DiagPackInvalid
		message := fmt.Sprintf("pack %s has an error and is not in effect", id)
		if g.omitReason == BlockedIntegrity {
			code = DiagPackIntegrity
			message = fmt.Sprintf("installed files for %s no longer match the lock; remove the pack and install it again", id)
		}
		if detail := strings.TrimSpace(g.omitDetail); detail != "" {
			message = message + ": " + detail
		}
		diags = append(diags, Diagnostic{
			Code: code, PackID: id,
			Message: message,
		})
	case !g.enabled:
		sum.BlockedReason = BlockedDisabled
		diags = append(diags, Diagnostic{
			Code: DiagPackDisabled, PackID: id,
			Message: fmt.Sprintf("pack %s disabled in desired state", id),
		})
	case !g.apiOK:
		sum.BlockedReason = BlockedCompatibility
		diags = append(diags, Diagnostic{
			Code: DiagExtensionAPIIncompatible, PackID: id,
			Message: fmt.Sprintf("pack %s requires extension API %s; host provides %s",
				id, pc.Manifest.Compatibility.ExtensionAPI, ExtensionAPIVersion),
		})
	case !g.scannersOK:
		sum.BlockedReason = BlockedRequiresScanners
		sum.UnmetRequiresScanners = append([]string(nil), g.scannersMiss...)
		for _, scannerID := range g.scannersMiss {
			diags = append(diags, Diagnostic{
				Code: DiagRequiresScannersUnmet, PackID: id, ScannerID: scannerID,
				Message: fmt.Sprintf("pack %s requires_scanners %q not enabled and runnable", id, scannerID),
			})
		}
	case !contributing[id]:
		sum.BlockedReason = BlockedRequires
		var unmet []string
		for _, req := range pc.Manifest.DependencyIDs() {
			req = strings.TrimSpace(req)
			if req == "" {
				continue
			}
			if !contributing[req] {
				unmet = append(unmet, req+" "+pc.Manifest.Dependencies[req].Version)
				continue
			}
			actual := versions[req]
			if !dependencyVersionSatisfied(pc.Manifest.Dependencies[req], actual) {
				unmet = append(unmet, fmt.Sprintf("%s %s (found %s)", req, pc.Manifest.Dependencies[req].Version, actual))
			}
		}
		diags = append(diags, Diagnostic{
			Code: DiagRequiresUnmet, PackID: id,
			Message: fmt.Sprintf("pack %s requires unmet: %s", id, strings.Join(unmet, ", ")),
		})
	}
	if pc.NeedsReload {
		sum.NeedsReload = true
		diags = append(diags, Diagnostic{
			Code: DiagPackNeedsReload, PackID: id,
			Message: fmt.Sprintf("linked pack %s changed on disk; Reload from disk to lock this version", id),
		})
	}
	return sum, diags
}

func collectContributions(packs []PackContent, contributing map[string]bool) (map[string][]UnitContribution, map[string]string) {
	contributions := map[string][]UnitContribution{}
	kindByID := map[string]string{}
	for _, pc := range packs {
		if !contributing[pc.Pack.ID] {
			continue
		}
		for _, u := range pc.Units {
			contributions[u.ID] = append(contributions[u.ID], UnitContribution{
				PackID:  pc.Pack.ID,
				Path:    u.Path,
				Content: u.Content,
			})
			kindByID[u.ID] = u.Kind
		}
	}
	for id := range contributions {
		sort.Slice(contributions[id], func(i, j int) bool {
			return contributions[id][i].PackID < contributions[id][j].PackID
		})
	}
	return contributions, kindByID
}

func resolveUnits(
	contributions map[string][]UnitContribution,
	kindByID map[string]string,
	disabledSet map[string]struct{},
	desired DesiredState,
) (units, loaded map[string]UnitEffective, diags []Diagnostic) {
	units = map[string]UnitEffective{}
	loaded = map[string]UnitEffective{}
	allIDs := map[string]struct{}{}
	for id := range contributions {
		allIDs[id] = struct{}{}
	}
	for id := range disabledSet {
		allIDs[id] = struct{}{}
	}
	for id := range desired.Own {
		allIDs[id] = struct{}{}
	}
	ids := make([]string, 0, len(allIDs))
	for id := range allIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		ue, more, keep := resolveOneUnit(id, contributions[id], kindByID[id], disabledSet, desired)
		diags = append(diags, more...)
		if !keep {
			continue
		}
		units[id] = ue
		if ue.Status == UnitStatusLoaded || ue.Status == UnitStatusOwned {
			loaded[id] = ue
		}
	}
	return units, loaded, diags
}

func resolveOneUnit(
	id string,
	prov []UnitContribution,
	kind string,
	disabledSet map[string]struct{},
	desired DesiredState,
) (UnitEffective, []Diagnostic, bool) {
	ue := UnitEffective{
		ID:            id,
		Kind:          kind,
		Contributions: append([]UnitContribution(nil), prov...),
	}
	if ue.Kind == "" {
		// Selector-only ids retain their encoded kind.
		ue.Kind = kindRootForRel(id)
	}
	var diags []Diagnostic

	if _, dis := disabledSet[id]; dis {
		ue.Status = UnitStatusDisabled
		diags = append(diags, Diagnostic{
			Code: DiagUnitDisabled, UnitID: id,
			Message: fmt.Sprintf("unit %s disabled in desired state", id),
		})
		return ue, diags, true
	}

	switch len(prov) {
	case 0:
		return ue, diags, false
	case 1:
		ue.Status = UnitStatusLoaded
		ue.WinnerPackID = prov[0].PackID
		ue.Content = prov[0].Content
		return ue, diags, true
	default:
		return resolveMultiContribution(id, ue, prov, desired)
	}
}

func resolveMultiContribution(
	id string,
	ue UnitEffective,
	prov []UnitContribution,
	desired DesiredState,
) (UnitEffective, []Diagnostic, bool) {
	var diags []Diagnostic
	ownPack := strings.TrimSpace(desired.Own[id])
	if ownPack == "" {
		ue.Status = UnitStatusConflict
		diags = append(diags, Diagnostic{
			Code: DiagConflict, UnitID: id,
			Message: fmt.Sprintf("unit %s conflict among %d contributions; not loaded", id, len(prov)),
		})
		return ue, diags, true
	}
	var winner *UnitContribution
	for i := range prov {
		if prov[i].PackID == ownPack {
			winner = &prov[i]
			break
		}
	}
	if winner == nil {
		ue.Status = UnitStatusConflict
		diags = append(diags, Diagnostic{
			Code: DiagOwnUnknownPack, UnitID: id, PackID: ownPack,
			Message: fmt.Sprintf("unit %s own:%s not among contributions", id, ownPack),
		})
		diags = append(diags, Diagnostic{
			Code: DiagConflict, UnitID: id,
			Message: fmt.Sprintf("unit %s conflict among %d contributions; not loaded", id, len(prov)),
		})
		return ue, diags, true
	}
	ue.Status = UnitStatusOwned
	ue.WinnerPackID = winner.PackID
	ue.Content = winner.Content
	diags = append(diags, Diagnostic{
		Code: DiagOwned, UnitID: id, PackID: winner.PackID,
		Message: fmt.Sprintf("unit %s selected from %s by own:", id, winner.PackID),
	})
	return ue, diags, true
}

// revisionDomain versions catalog interpretation and encoding.
const revisionDomain = "painted-wolf/catalog-revision/1"

// revisionOf hashes canonical length-prefixed resolve inputs.
func revisionOf(e *EffectiveCatalog, packs []PackContent) string {
	h := sha256.New()
	field := func(label string, body []byte) {
		_, _ = fmt.Fprintf(h, "%s\x1f%d\x1f", label, len(body))
		_, _ = h.Write(body)
	}
	str := func(label, body string) { field(label, []byte(body)) }

	str("domain", revisionDomain)
	// Compiler/schema version, unconditionally.
	str("extension_api", ExtensionAPIVersion)
	str("manifest_version", strconv.Itoa(ManifestVersion))
	str("desired_format", strconv.Itoa(DesiredFormat))
	str("lock_format", strconv.Itoa(LockFormat))

	// Winning units: id, provider, status, exact bytes.
	for _, id := range e.LoadedUnitIDs() {
		u := e.Loaded[id]
		str("unit", id)
		str("winner", u.WinnerPackID)
		str("status", string(u.Status))
		field("content", u.Content)
	}

	// Effective extension configuration: the resolved desired state.
	desired := e.Desired
	str("desired.format", strconv.Itoa(desired.Format))
	for _, p := range desired.Packs {
		str("desired.pack", p.ID)
		str("desired.source", p.Source)
		str("desired.version", p.Version)
		str("desired.ref", p.Ref)
		str("desired.development", strconv.FormatBool(p.Development))
		if p.Enabled != nil {
			str("desired.enabled", strconv.FormatBool(*p.Enabled))
		}
	}
	disabled := append([]string(nil), desired.Disabled...)
	sort.Strings(disabled)
	for _, id := range disabled {
		str("desired.disabled", id)
	}
	ownIDs := make([]string, 0, len(desired.Own))
	for id := range desired.Own {
		ownIDs = append(ownIDs, id)
	}
	sort.Strings(ownIDs)
	for _, id := range ownIDs {
		str("desired.own", id)
		str("desired.own.pack", desired.Own[id])
	}
	configPacks := make([]string, 0, len(desired.Configuration))
	for id := range desired.Configuration {
		configPacks = append(configPacks, id)
	}
	sort.Strings(configPacks)
	for _, packID := range configPacks {
		properties := desired.Configuration[packID]
		names := make([]string, 0, len(properties))
		for name := range properties {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			str("desired.config.pack", packID)
			str("desired.config.name", name)
			str("desired.config.value", CanonicalConfigurationValue(properties[name]))
		}
	}

	// Exact package integrity identities plus gate outcomes.
	for _, pc := range packs {
		str("pack", pc.Pack.ID)
		str("pack.version", pc.Manifest.Version)
		str("pack.kind", string(pc.Kind))
		if pc.Locked != nil {
			str("pack.revision", pc.Locked.Revision)
			str("pack.integrity", pc.Locked.Integrity)
		}
		str("pack.needs_reload", strconv.FormatBool(pc.NeedsReload))
		str("pack.omit", string(pc.OmitReason))
	}
	for _, sum := range e.Packs {
		str("gate", sum.ID)
		str("gate.contributing", strconv.FormatBool(sum.Contributing))
		str("gate.blocked", string(sum.BlockedReason))
		str("gate.needs_reload", strconv.FormatBool(sum.NeedsReload))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// CanonicalConfigurationValue renders one configuration value with a type tag
// so distinct typed values can never encode to the same string.
func CanonicalConfigurationValue(value any) string {
	switch v := value.(type) {
	case bool:
		return "b:" + strconv.FormatBool(v)
	case int:
		return "i:" + strconv.Itoa(v)
	case int64:
		return "i:" + strconv.FormatInt(v, 10)
	case uint64:
		return "i:" + strconv.FormatUint(v, 10)
	case float64:
		return "f:" + strconv.FormatFloat(v, 'g', -1, 64)
	case string:
		return "s:" + v
	case []string:
		return "l:" + strings.Join(v, "\x1f")
	case []any:
		parts := make([]string, 0, len(v))
		for _, member := range v {
			s, _ := member.(string)
			parts = append(parts, s)
		}
		return "l:" + strings.Join(parts, "\x1f")
	default:
		return fmt.Sprintf("?:%v", v)
	}
}
