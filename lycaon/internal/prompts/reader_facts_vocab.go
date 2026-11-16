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
	return readerFactVocabCache.get(catalogCacheKey(eff), func(string) (ReaderFactVocabulary, error) {
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
		}
	}
	return false
}

// CheckTemplateVocabulary validates reserved-prefix variable references across
// every template and skill unit in eff. Stock units fail load on an undeclared
// reference; third-party units record a diagnostic note.
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

func checkTemplateVocabularyUncached(eff *extpacks.EffectiveCatalog) ([]string, error) {
	vocab, err := BuildReaderFactVocabulary(eff)
	if err != nil {
		return nil, err
	}
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
	}
	sort.Strings(notes)
	return notes, nil
}
