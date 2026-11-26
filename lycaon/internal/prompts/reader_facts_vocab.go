package prompts

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
)

const agentSkillMembershipPrefix = "agent_has_skill_"

// ReaderFactScalarSpec describes one scalar reader fact declared in reader-facts.yaml.
type ReaderFactScalarSpec struct {
	Name   string `yaml:"name"`
	Type   string `yaml:"type"`
	States string `yaml:"states"`
}

// ReaderFactFamilySpec describes one reader-fact family declared in reader-facts.yaml.
type ReaderFactFamilySpec struct {
	Pattern string `yaml:"pattern"`
	Prefix  string `yaml:"-"`
	Nested  bool   `yaml:"-"`
	Over    string `yaml:"over"`
	Type    string `yaml:"type"`
	States  string `yaml:"states"`
}

var stockSkillMembershipKeys = sync.OnceValue(func() map[string]string {
	out := map[string]string{}
	eff, err := extpacks.CatalogForConsumers()
	if err != nil || eff == nil {
		return out
	}
	for _, id := range eff.LoadedUnitIDs() {
		name, ok := strings.CutPrefix(id, "skills/")
		if !ok || name == "" {
			continue
		}
		if u, ok := eff.Loaded[id]; ok && eff.StockAuthority(u.WinnerPackID) {
			out[name] = agentSkillMembershipPrefix + strings.ReplaceAll(name, "-", "_")
		}
	}
	return out
})

func agentSkillMembershipKey(name string) string {
	name = strings.TrimSpace(name)
	return stockSkillMembershipKeys()[name]
}

// HasReservedReaderFactPrefix reports whether ref begins with a reserved reader-fact prefix.
func HasReservedReaderFactPrefix(ref string) bool {
	ref = strings.TrimSpace(ref)
	for _, prefix := range reservedReaderFactPrefixes {
		if strings.HasPrefix(ref, prefix) {
			return true
		}
	}
	return false
}

// IsDeclaredOrReservedReaderFact reports whether key is a declared reader fact
// or lies under a reserved reader-fact prefix.
func IsDeclaredOrReservedReaderFact(key string) bool {
	key = strings.TrimSpace(key)
	if _, ok := declaredReaderFactScalars[key]; ok {
		return true
	}
	return HasReservedReaderFactPrefix(key)
}

// Vars merges reader.Facts() with caller-supplied occurrence data, rejecting
// any occurrence key that collides with a declared or reserved reader fact.
func Vars(reader Reader, occurrence map[string]any) (map[string]any, error) {
	if !reader.Valid() {
		return nil, fmt.Errorf("prompt vars require a valid reader")
	}
	if len(occurrence) > 0 {
		keys := make([]string, 0, len(occurrence))
		for k := range occurrence {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if IsDeclaredOrReservedReaderFact(k) {
				return nil, fmt.Errorf("occurrence key %q is a declared or reserved reader fact", k)
			}
		}
	}
	out := reader.Facts()
	for k, v := range occurrence {
		out[k] = v
	}
	if !slices.Contains(reader.Offered(), "skills_read") {
		clearSkillRosterWithoutSkillsRead(out)
	}
	return withAgentSkillMembershipVars(out), nil
}

// ReaderFactVocabulary holds the expanded family sets for a catalog.
type ReaderFactVocabulary struct {
	Scalars       map[string]ReaderFactScalarSpec
	Families      []ReaderFactFamilySpec
	Tools         map[string]bool
	ReceiptTools  map[string]bool
	EvidenceKinds map[string]bool
	Skills        map[string]bool
	SkillKeys     []string
}

var cachedNativeManifest = sync.OnceValues(nativemanifest.Load)

// BuildReaderFactVocabulary resolves the family domains for eff.
func BuildReaderFactVocabulary(eff *extpacks.EffectiveCatalog) (ReaderFactVocabulary, error) {
	if eff == nil {
		var err error
		eff, err = extpacks.CatalogForConsumers()
		if err != nil {
			return ReaderFactVocabulary{}, err
		}
	}
	return readerFactVocabCache.get(catalogRevisionKey(eff), func(string) (ReaderFactVocabulary, error) {
		return buildReaderFactVocabularyUncached(eff)
	})
}

func buildReaderFactVocabularyUncached(eff *extpacks.EffectiveCatalog) (ReaderFactVocabulary, error) {
	tools := map[string]bool{}
	for _, name := range currentToolNames() {
		tools[name] = true
	}
	if schemas, err := loadToolSchemaCached(eff); err == nil && schemas != nil {
		for name := range schemas.Tools {
			if toolschema.ValidToolName(name) {
				tools[name] = true
			}
		}
	}
	for _, id := range eff.LoadedUnitIDs() {
		if name, ok := strings.CutPrefix(id, "tools/schemas/"); ok && toolschema.ValidToolName(name) {
			tools[name] = true
		}
	}
	if profiles, err := declaredToolProfiles(eff); err == nil {
		for _, p := range profiles {
			for name := range p.Tools {
				if toolschema.ValidToolName(name) {
					tools[name] = true
				}
			}
		}
	}
	if cfg, err := cachedNativeManifest(); err == nil {
		for _, name := range cfg.AllTools() {
			tools[name] = true
		}
		for _, name := range cfg.HostProducedTools {
			tools[name] = true
		}
	}
	receiptTools := maps.Clone(tools)
	binding := evidence.ActiveBinding()
	for _, name := range binding.DeclaredToolNames() {
		receiptTools[name] = true
	}

	kinds := map[string]bool{}
	for kind := range binding.KindShapeMap() {
		kinds[kind] = true
	}

	skillSet := map[string]bool{}
	skillKeySet := map[string]bool{}
	for _, id := range eff.LoadedUnitIDs() {
		name, ok := strings.CutPrefix(id, "skills/")
		if !ok || name == "" {
			continue
		}
		norm := strings.ReplaceAll(name, "-", "_")
		skillSet[name] = true
		skillSet[norm] = true
		if u, ok := eff.Loaded[id]; ok && eff.StockAuthority(u.WinnerPackID) {
			skillKeySet[norm] = true
		}
	}

	loadedSkills, _ := extpacks.LoadEffectiveSkills(eff)
	for _, sk := range loadedSkills {
		if sk.Name != "" {
			norm := strings.ReplaceAll(sk.Name, "-", "_")
			skillSet[sk.Name] = true
			skillSet[norm] = true
			if !sk.UserProvided && eff.StockAuthority(sk.PackID) {
				skillKeySet[norm] = true
			}
		}
	}
	skillKeys := make([]string, 0, len(skillKeySet))
	for k := range skillKeySet {
		skillKeys = append(skillKeys, k)
	}
	sort.Strings(skillKeys)
	return ReaderFactVocabulary{
		Scalars:       DeclaredReaderFactScalars(),
		Families:      ReaderFactFamilies(),
		Tools:         tools,
		ReceiptTools:  receiptTools,
		EvidenceKinds: kinds,
		Skills:        skillSet,
		SkillKeys:     skillKeys,
	}, nil
}

// AllowsReference reports whether ref is a declared scalar or family member.
func (v ReaderFactVocabulary) AllowsReference(ref string) bool {
	if _, ok := declaredReaderFactScalars[ref]; ok {
		return true
	}
	for _, fam := range readerFactFamilies {
		var member string
		var matched bool
		if fam.Nested {
			if ref == fam.Prefix {
				return true
			}
			member, matched = strings.CutPrefix(ref, fam.Prefix+".")
		} else {
			member, matched = strings.CutPrefix(ref, fam.Prefix)
		}
		if !matched || member == "" {
			continue
		}
		switch fam.Over {
		case "tools":
			if fam.Prefix == "receipt_kind" {
				if v.ReceiptTools[member] {
					return true
				}
			} else if v.Tools[member] {
				return true
			}
		case "evidence_kinds":
			if v.EvidenceKinds[member] {
				return true
			}
		case "skills":
			if v.Skills[member] {
				return true
			}
		case "command_programs":
			if toolcontract.IsDeclaredCommandProgram(member) {
				return true
			}
		}
	}
	return false
}

// CheckTemplateVocabulary validates reserved-prefix variable references and
// forbids capability {% set %} derivations across every template and skill
// unit in eff. Stock units fail load on a violation; third-party units record
// a diagnostic note.
func CheckTemplateVocabulary(eff *extpacks.EffectiveCatalog) ([]string, error) {
	if eff == nil {
		return nil, fmt.Errorf("nil effective catalog")
	}
	notes, err := templateVocabCache.get(catalogCacheKey(eff), func(string) ([]string, error) {
		return checkTemplateVocabularyUncached(eff)
	})
	if err != nil {
		return nil, err
	}
	return slices.Clone(notes), nil
}

// DeclaredReaderFactScalars returns a copy of the declared reader-fact scalar specs from reader-facts.yaml.
func DeclaredReaderFactScalars() map[string]ReaderFactScalarSpec {
	return maps.Clone(declaredReaderFactScalars)
}

// ReaderFactFamilies returns a copy of the declared reader-fact family specs from reader-facts.yaml.
func ReaderFactFamilies() []ReaderFactFamilySpec {
	return slices.Clone(readerFactFamilies)
}

// IsBooleanReaderFact reports whether ref is declared as a boolean reader scalar or family in reader-facts.yaml.
func IsBooleanReaderFact(ref string) bool {
	if spec, ok := declaredReaderFactScalars[ref]; ok {
		return spec.Type == "bool"
	}
	for _, fam := range readerFactFamilies {
		if fam.Nested {
			if ref == fam.Prefix || strings.HasPrefix(ref, fam.Prefix+".") {
				return fam.Type == "bool"
			}
		} else if strings.HasPrefix(ref, fam.Prefix) {
			return fam.Type == "bool"
		}
	}
	return false
}

// ResolveCatalogTemplateUnit resolves a template ref to its loaded unit in eff.
func ResolveCatalogTemplateUnit(eff *extpacks.EffectiveCatalog, templateRef string) (string, extpacks.UnitEffective, bool) {
	id, ok := TemplateUnitID(eff, templateRef)
	if !ok {
		return "", extpacks.UnitEffective{}, false
	}
	return id, eff.Loaded[id], true
}

func checkSetBindings(unitID, origin, source string, vocab ReaderFactVocabulary, stock bool) ([]string, error) {
	var notes []string
	report := func(msg string) error {
		if stock {
			return fmt.Errorf("template %s (%s): %s", unitID, origin, msg)
		}
		notes = append(notes, fmt.Sprintf("%s (%s): %s", unitID, origin, msg))
		return nil
	}
	tree, err := pongoplain.ParseTemplateTree(source)
	if err != nil {
		if rerr := report(fmt.Sprintf("template grammar error: %v", err)); rerr != nil {
			return nil, rerr
		}
		return notes, nil
	}
	tainted := map[string]bool{}
	taintedBool := map[string]bool{}
	checkBinding := func(b pongoplain.Binding) error {
		if IsDeclaredOrReservedReaderFact(b.Name) || strings.HasPrefix(b.Name, "can_") || strings.HasPrefix(b.Name, "has_") {
			return report(fmt.Sprintf("capability binding %q is forbidden", b.Name))
		}
		refsReaderFact := false
		refsBoolFact := false
		hasOps := b.Expr.HasLogicalOrCompare()
		for _, ref := range b.Expr.Identifiers() {
			root := ref
			if idx := strings.IndexByte(ref, '.'); idx >= 0 {
				root = ref[:idx]
			}
			if tainted[ref] || tainted[root] {
				refsReaderFact = true
				if taintedBool[ref] || taintedBool[root] {
					refsBoolFact = true
				}
			}
			if IsDeclaredOrReservedReaderFact(ref) || IsDeclaredOrReservedReaderFact(root) || vocab.AllowsReference(ref) {
				refsReaderFact = true
				if IsBooleanReaderFact(ref) || IsBooleanReaderFact(root) {
					refsBoolFact = true
				}
			}
		}
		if refsReaderFact {
			tainted[b.Name] = true
			if hasOps || refsBoolFact {
				taintedBool[b.Name] = true
			}
		}
		if (hasOps && refsReaderFact) || refsBoolFact {
			return report(fmt.Sprintf("capability alias/derivation %q (%s) is forbidden", b.Name, b.RawExpr))
		}
		return nil
	}
	var walkNodes func([]*pongoplain.TemplateNode) error
	walkNodes = func(nodes []*pongoplain.TemplateNode) error {
		for _, n := range nodes {
			for _, b := range n.Bindings {
				if err := checkBinding(b); err != nil {
					return err
				}
			}
			for _, br := range n.Branches {
				if err := walkNodes(br.Body); err != nil {
					return err
				}
			}
			if err := walkNodes(n.Body); err != nil {
				return err
			}
			if err := walkNodes(n.EmptyBody); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walkNodes(tree.Nodes); err != nil {
		return nil, err
	}
	return notes, nil
}

func buildDeclaredToolAndOpSet(vocab ReaderFactVocabulary) map[string]bool {
	declared := maps.Clone(vocab.Tools)
	if declared == nil {
		declared = map[string]bool{}
	}
	if cfg, err := cachedNativeManifest(); err == nil {
		for _, list := range [][]string{cfg.AllTools(), cfg.HostProducedTools, cfg.AllOperations()} {
			for _, name := range list {
				if name = strings.TrimSpace(name); name != "" {
					declared[name] = true
				}
			}
		}
	}
	return declared
}

func checkZeroToolUtilitySource(unitID, origin, source string, vocab ReaderFactVocabulary, declared map[string]bool, stock bool) ([]string, error) {
	var notes []string
	report := func(msg string) error {
		if stock {
			return fmt.Errorf("template %s (%s): %s", unitID, origin, msg)
		}
		notes = append(notes, fmt.Sprintf("%s (%s): %s", unitID, origin, msg))
		return nil
	}
	for _, ref := range pongoplain.VariableReferences(source) {
		root := ref
		if idx := strings.IndexByte(ref, '.'); idx >= 0 {
			root = ref[:idx]
		}
		if IsCapabilityKeyName(ref) || vocab.AllowsReference(ref) || root == "units" {
			if err := report(fmt.Sprintf("zero-tool utility prompt references reader/capability fact %q", ref)); err != nil {
				return nil, err
			}
		}
	}
	for _, tool := range StructuralActionClaims(source, declared) {
		if err := report(fmt.Sprintf("zero-tool utility prompt claims callable tool or command %q", tool)); err != nil {
			return nil, err
		}
	}
	return notes, nil
}

func checkStructuralFragmentTransitive(eff *extpacks.EffectiveCatalog, unitID, origin, source string, vocab ReaderFactVocabulary, declared map[string]bool, stock bool, seen map[string]bool) ([]string, error) {
	var notes []string
	report := func(msg string) error {
		if stock {
			return fmt.Errorf("template %s (%s): %s", unitID, origin, msg)
		}
		notes = append(notes, fmt.Sprintf("%s (%s): %s", unitID, origin, msg))
		return nil
	}
	for _, ref := range pongoplain.VariableReferences(source) {
		if IsCapabilityKeyName(ref) || vocab.AllowsReference(ref) {
			if err := report(fmt.Sprintf("structural fragment references capability fact %q", ref)); err != nil {
				return nil, err
			}
		}
	}
	for _, action := range StructuralActionClaims(source, declared) {
		if err := report(fmt.Sprintf("structural fragment claims callable tool or command %q", action)); err != nil {
			return nil, err
		}
	}
	tree, err := pongoplain.ParseTemplateTree(source)
	if err != nil {
		if rErr := report(fmt.Sprintf("structural fragment could not be parsed for include checks: %v", err)); rErr != nil {
			return nil, rErr
		}
		return notes, nil
	}
	if eff == nil {
		return notes, nil
	}
	var walk func([]*pongoplain.TemplateNode) error
	walk = func(nodes []*pongoplain.TemplateNode) error {
		for _, n := range nodes {
			if n.Kind == pongoplain.NodeInclude {
				incID, incUnit, ok := ResolveCatalogTemplateUnit(eff, n.IncludeRef)
				if ok && !seen[incID] {
					seen[incID] = true
					incNotes, err := checkStructuralFragmentTransitive(eff, unitID+" -> "+incID, incUnit.WinnerPackID, string(incUnit.Content), vocab, declared, stock, seen)
					if err != nil {
						return err
					}
					notes = append(notes, incNotes...)
				}
			}
			for _, br := range n.Branches {
				if err := walk(br.Body); err != nil {
					return err
				}
			}
			if err := walk(n.Body); err != nil {
				return err
			}
			if err := walk(n.EmptyBody); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(tree.Nodes); err != nil {
		return nil, err
	}
	return notes, nil
}

func checkTemplateVocabularyUncached(eff *extpacks.EffectiveCatalog) ([]string, error) {
	vocab, err := BuildReaderFactVocabulary(eff)
	if err != nil {
		return nil, err
	}
	declaredTools := buildDeclaredToolAndOpSet(vocab)
	var notes []string
	checkSource := func(unitID, packID, source string, stock bool) error {
		refs := pongoplain.VariableReferences(source)
		for _, ref := range refs {
			if !HasReservedReaderFactPrefix(ref) {
				continue
			}
			if vocab.AllowsReference(ref) {
				continue
			}
			if stock {
				return fmt.Errorf("template %s (%s): undeclared reader fact %q", unitID, packID, ref)
			}
			notes = append(notes, fmt.Sprintf("%s (%s): undeclared reader fact %q", unitID, packID, ref))
		}
		setNotes, err := checkSetBindings(unitID, packID, source, vocab, stock)
		if err != nil {
			return err
		}
		notes = append(notes, setNotes...)
		if subRef, ok := strings.CutPrefix(strings.TrimSuffix(unitID, ".md"), "guidance/"); ok {
			if IsZeroToolUtilityTemplate(subRef) {
				utilNotes, err := checkZeroToolUtilitySource(unitID, packID, source, vocab, declaredTools, stock)
				if err != nil {
					return err
				}
				notes = append(notes, utilNotes...)
			}
			if IsStructuralGuidanceFragment(subRef) || IsStructuralInjectFragment(subRef) {
				structNotes, err := checkStructuralFragmentTransitive(eff, unitID, packID, source, vocab, declaredTools, stock, map[string]bool{unitID: true})
				if err != nil {
					return err
				}
				notes = append(notes, structNotes...)
			}
		}
		return nil
	}

	for _, id := range eff.LoadedUnitIDs() {
		u, ok := eff.Loaded[id]
		if !ok {
			continue
		}
		stock := eff.StockAuthority(u.WinnerPackID)
		switch {
		case strings.HasPrefix(id, "agents/prompts/"),
			strings.HasPrefix(id, "shared/partials/"),
			strings.HasPrefix(id, "shared/archetypes/"):
			if err := checkSource(id, u.WinnerPackID, string(u.Content), stock); err != nil {
				return nil, err
			}
		case strings.HasPrefix(id, "shared/units/"):
			body := promptunit.Body(u.Content)
			if err := checkSource(id, u.WinnerPackID, string(body), stock); err != nil {
				return nil, err
			}
		case strings.HasPrefix(id, "guidance/") && !strings.HasPrefix(id, "guidance/gate-feedback/"):
			if path, ok := eff.UnitPath(id); ok && !strings.HasSuffix(path.String(), ".md") {
				continue
			}
			if err := checkSource(id, u.WinnerPackID, string(u.Content), stock); err != nil {
				return nil, err
			}
		}
	}

	if eff.StockAuthority("painted-wolf/platform") {
		if _, hasPlatformGuidance := eff.Loaded["guidance/utility/today-line"]; hasPlatformGuidance {
			for _, ref := range ZeroToolUtilityTemplates() {
				if _, exists := eff.Loaded["guidance/"+ref]; !exists {
					return nil, fmt.Errorf("zero-tool utility allowlist ref %q missing from loaded catalog", ref)
				}
			}
			for _, ref := range StructuralGuidanceFragments() {
				if _, exists := eff.Loaded["guidance/"+ref]; !exists {
					raw, _, bErr := (BundledLayout{}).ReadBundled("guidance/" + ref + ".md")
					if bErr != nil {
						return nil, fmt.Errorf("structural guidance allowlist ref %q missing from loaded catalog", ref)
					}
					if _, tErr := checkStructuralFragmentTransitive(eff, "guidance/"+ref, "painted-wolf/platform", string(raw), vocab, declaredTools, true, map[string]bool{"guidance/" + ref: true}); tErr != nil {
						return nil, tErr
					}
				}
			}
			for _, ref := range StructuralInjectFragments() {
				if _, exists := eff.Loaded["guidance/"+ref]; !exists {
					return nil, fmt.Errorf("structural inject allowlist ref %q missing from loaded catalog", ref)
				}
			}
		}
	}

	loadedSkills, _ := extpacks.LoadEffectiveSkills(eff)
	for _, sk := range loadedSkills {
		stock := !sk.UserProvided && eff.StockAuthority(sk.PackID)
		unitID := "skills/" + sk.Name
		if err := checkSource(unitID, sk.PackID, sk.Body, stock); err != nil {
			return nil, err
		}
		for _, res := range sk.MarkdownResources() {
			raw, err := sk.ReadResource(res)
			if err != nil {
				continue
			}
			if err := checkSource(unitID+"/"+res, sk.PackID, string(raw), stock); err != nil {
				return nil, err
			}
		}
	}
	sort.Strings(notes)
	return notes, nil
}

func checkCapturedOverlayTemplates(eff *extpacks.EffectiveCatalog, captured map[string][]byte) ([]string, error) {
	vocab, err := BuildReaderFactVocabulary(eff)
	if err != nil {
		return nil, err
	}
	declaredTools := buildDeclaredToolAndOpSet(vocab)
	var notes []string
	refs := make([]string, 0, len(captured))
	for ref := range captured {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		if !strings.HasSuffix(ref, ".md") {
			continue
		}
		body := captured[ref]
		if strings.HasPrefix(ref, "units/") || strings.HasPrefix(ref, "shared/units/") {
			body = promptunit.Body(body)
		}
		varRefs := pongoplain.VariableReferences(string(body))
		for _, vref := range varRefs {
			if !HasReservedReaderFactPrefix(vref) {
				continue
			}
			if vocab.AllowsReference(vref) {
				continue
			}
			notes = append(notes, fmt.Sprintf("%s (override): undeclared reader fact %q", ref, vref))
		}
		setNotes, _ := checkSetBindings(ref, "override", string(body), vocab, false)
		notes = append(notes, setNotes...)
		if subRef, ok := strings.CutPrefix(strings.TrimSuffix(ref, ".md"), "guidance/"); ok {
			if IsZeroToolUtilityTemplate(subRef) {
				utilNotes, _ := checkZeroToolUtilitySource(ref, "override", string(body), vocab, declaredTools, false)
				notes = append(notes, utilNotes...)
			}
			if IsStructuralGuidanceFragment(subRef) || IsStructuralInjectFragment(subRef) {
				structNotes, _ := checkStructuralFragmentTransitive(eff, ref, "override", string(body), vocab, declaredTools, false, map[string]bool{ref: true})
				notes = append(notes, structNotes...)
			}
		}
	}
	sort.Strings(notes)
	return notes, nil
}
