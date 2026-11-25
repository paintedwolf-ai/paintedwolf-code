package interactions

import (
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/toolschema"
)

// ConditionSite is one guard of a shipped template, skill or OAR rule. ID is
// the branch identity coverage observations carry.
type ConditionSite struct {
	ID             string `json:"id"`
	Family         string `json:"family"`
	ContributionID string `json:"contribution_id"`
	WinnerPackID   string `json:"winner_pack_id"`
	SourceSpan     string `json:"source_span"`
	// Includers are the include tags (unit:line) that deliver the guard's unit.
	Includers []string `json:"includers,omitempty"`
	// PrecedingArms are the earlier arms of the same if; one that holds
	// bypasses this guard.
	PrecedingArms []string `json:"preceding_arms,omitempty"`
	InLoop        bool     `json:"in_loop,omitempty"`
	Source        string   `json:"source"`
	Atoms         []string `json:"atoms"`
}

// templateKinds maps each catalog kind root whose Markdown the composed
// prompt engine reads as templates to the ref prefix renders name it by.
var templateKinds = map[string]string{
	"agents/prompts":    "agents/",
	"shared/partials":   "partials/",
	"shared/units":      "units/",
	"shared/archetypes": "archetypes/",
	"guidance":          "guidance/",
}

// TemplateUnits lists every template the prompt engine renders from eff, with
// the source it renders: the loaded, unarchived Markdown units by unit ID,
// and the reserved `_` partials of contributing packs by the ref that reads
// them, each confirmed through the production bundled read.
func TemplateUnits(eff *extpacks.EffectiveCatalog) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range eff.LoadedUnitIDs() {
		if _, _, archived := extpacks.SplitArchiveUnitID(id); archived {
			continue
		}
		if templateKind(id) == "" {
			continue
		}
		at, ok := eff.UnitPath(id)
		if !ok || path.Ext(at.String()) != ".md" {
			continue
		}
		raw, _, _ := eff.UnitContent(id)
		if strings.HasPrefix(id, "shared/units/") {
			raw = promptunit.Body(raw)
		}
		out[id] = string(raw)
	}
	packs, err := extpacks.DiscoverEffective(eff)
	if err != nil {
		return nil, err
	}
	for _, p := range packs {
		for kind, prefix := range templateKinds {
			dir := p.Root.Join(strings.Split(kind, "/")...)
			if !dir.IsDir() {
				continue
			}
			err := dir.Walk(func(src extpacks.Source, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), "_") || path.Ext(d.Name()) != ".md" {
					return err
				}
				ref := prefix + src.RelTo(dir)
				raw, at, rerr := prompts.BundledLayout{Catalog: eff}.ReadBundled(ref)
				if rerr != nil {
					return fmt.Errorf("reserved partial %s: %w", ref, rerr)
				}
				if at.String() != src.String() {
					return fmt.Errorf("reserved partial %s reads %s, not %s", ref, at, src)
				}
				out[ref] = string(raw)
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func templateKind(unitID string) string {
	for kind := range templateKinds {
		if strings.HasPrefix(unitID, kind+"/") {
			return kind
		}
	}
	return ""
}

// TemplateIdentity names the registered template a render's ref reads: the
// catalog unit the production resolution picks, or the ref itself for a
// reserved partial.
func TemplateIdentity(eff *extpacks.EffectiveCatalog, templates map[string]string, ref string) (string, bool) {
	if id, ok := prompts.TemplateUnitID(eff, ref); ok {
		return id, true
	}
	if path.Ext(ref) == "" {
		ref += ".md"
	}
	_, ok := templates[ref]
	return ref, ok
}

// DiscoverConditionSites walks the effective templates, skills and compiled
// OAR rules of eff and returns every guard with its atoms, whether or not the
// text it guards names an action.
func DiscoverConditionSites(eff *extpacks.EffectiveCatalog, schemas *toolschema.Config) ([]ConditionSite, error) {
	sites, err := templateConditionSitesOf(eff)
	if err != nil {
		return nil, err
	}
	ruleSites, err := oarConditionSites(eff, schemas)
	if err != nil {
		return nil, err
	}
	sites = append(sites, ruleSites...)
	slices.SortFunc(sites, func(a, b ConditionSite) int { return strings.Compare(a.ID, b.ID) })
	return sites, nil
}

// templateConditionSitesOf lists the guards of eff's templates and skills.
func templateConditionSitesOf(eff *extpacks.EffectiveCatalog) ([]ConditionSite, error) {
	var sites []ConditionSite
	includers := map[string][]string{}
	units, err := TemplateUnits(eff)
	if err != nil {
		return nil, err
	}
	for _, id := range sortedKeysOf(boolKeys(units)) {
		_, pack, _ := eff.UnitContent(id)
		found, err := templateConditionSites("template", id, pack, units[id], pongoplain.Composed, func(line int, ref string) {
			if inc, ok := TemplateIdentity(eff, units, ref); ok {
				includers[inc] = append(includers[inc], fmt.Sprintf("%s:L%d", id, line))
			}
		})
		if err != nil {
			return nil, err
		}
		sites = append(sites, found...)
	}
	for i := range sites {
		sites[i].Includers = includers[sites[i].ContributionID]
	}

	skillsList, _ := extpacks.LoadEffectiveSkills(eff)
	for _, sk := range skillsList {
		if sk.Project {
			continue
		}
		pack := "stock"
		if u, ok := eff.Loaded["skills/"+sk.Name]; ok && u.WinnerPackID != "" {
			pack = u.WinnerPackID
		}
		bodies := map[string]string{"skills/" + sk.Name + "/SKILL.md": sk.Body}
		for _, rel := range sk.MarkdownResources() {
			raw, err := sk.ReadResource(rel)
			if err != nil {
				return nil, err
			}
			bodies["skills/"+sk.Name+"/"+rel] = string(raw)
		}
		for _, id := range sortedKeysOf(boolKeys(bodies)) {
			found, err := templateConditionSites("skill", id, pack, bodies[id], pongoplain.Isolated, nil)
			if err != nil {
				return nil, err
			}
			sites = append(sites, found...)
		}
	}

	slices.SortFunc(sites, func(a, b ConditionSite) int { return strings.Compare(a.ID, b.ID) })
	return sites, nil
}

func boolKeys[V any](m map[string]V) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// templateConditionSites pairs each if/elif arm of the parsed tree with the
// branch the static inspection locates, in source order, so its ID matches
// the branch recorder's.
func templateConditionSites(family, unitID, packID, source string, profile pongoplain.Profile, onInclude func(line int, ref string)) ([]ConditionSite, error) {
	analysis, err := pongoplain.Inspect(source, profile)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", unitID, err)
	}
	var specs []pongoplain.BranchSpec
	for _, sp := range analysis.Branches {
		if sp.Kind == pongoplain.BranchIf || sp.Kind == pongoplain.BranchElif {
			specs = append(specs, sp)
		}
	}
	tree, err := pongoplain.ParseTemplateTree(source)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", unitID, err)
	}
	var arms []ConditionSite
	var walk func(nodes []*pongoplain.TemplateNode, inLoop bool)
	walk = func(nodes []*pongoplain.TemplateNode, inLoop bool) {
		for _, n := range nodes {
			switch n.Kind {
			case pongoplain.NodeIf:
				var preceding []string
				for _, arm := range n.Branches {
					if arm.Expr != nil {
						site := ConditionSite{
							Family: family, ContributionID: unitID, WinnerPackID: packID,
							SourceSpan: fmt.Sprintf("%s:L%d", unitID, arm.Line), InLoop: inLoop,
							PrecedingArms: slices.Clone(preceding), Source: arm.Expr.Source(),
						}
						for _, atom := range arm.Expr.Atoms() {
							site.Atoms = append(site.Atoms, atom.Source())
						}
						arms = append(arms, site)
						preceding = append(preceding, site.Source)
					}
					walk(arm.Body, inLoop)
				}
			case pongoplain.NodeFor:
				walk(n.Body, true)
				walk(n.EmptyBody, inLoop)
			case pongoplain.NodeWith:
				walk(n.Body, inLoop)
			case pongoplain.NodeInclude:
				if onInclude != nil {
					onInclude(n.Line, n.IncludeRef)
				}
			case pongoplain.NodeText, pongoplain.NodeOutput, pongoplain.NodeSet:
			}
		}
	}
	walk(tree.Nodes, false)
	if len(arms) != len(specs) {
		return nil, fmt.Errorf("%s: %d parsed arms but %d inspected if/elif branches", unitID, len(arms), len(specs))
	}
	for i, sp := range specs {
		if fmt.Sprintf("%s:L%d", unitID, sp.Line) != arms[i].SourceSpan {
			return nil, fmt.Errorf("%s: arm %d parsed at %s but inspected at line %d", unitID, i, arms[i].SourceSpan, sp.Line)
		}
		arms[i].ID = fmt.Sprintf("%s@L%d:C%d:%s", unitID, sp.Line, sp.Col, sp.Kind)
	}
	return arms, nil
}

// oarConditionSites lists the guards of every effective rule of eff.
func oarConditionSites(eff *extpacks.EffectiveCatalog, schemas *toolschema.Config) ([]ConditionSite, error) {
	rs, err := loadEffectiveRules(eff, schemas)
	if err != nil {
		return nil, err
	}
	return RuleConditionSites(rs.All(), func(r *oar.Rule) string {
		if u, ok := eff.Loaded["policy/"+r.ID]; ok && u.WinnerPackID != "" {
			return u.WinnerPackID
		}
		return "stock"
	})
}

// RuleConditionSites lists each rule's compiled condition and every nested
// step and alternative guard, attributed to the pack packOf names.
func RuleConditionSites(rules []*oar.Rule, packOf func(*oar.Rule) string) ([]ConditionSite, error) {
	env, err := oar.InstalledEngineDeclaration().Environment()
	if err != nil {
		return nil, err
	}
	var out []ConditionSite
	add := func(rule *oar.Rule, branch, source string) {
		site := ConditionSite{
			ID: rule.Qualified() + "#" + branch, Family: "oar", ContributionID: "policy/" + rule.ID,
			WinnerPackID: packOf(rule), SourceSpan: "policy/" + rule.ID + ":" + branch, Source: source,
		}
		if compiled, err := oarcore.CompileCondition(source, env); err == nil {
			site.Atoms = compiled.Atoms()
		}
		out = append(out, site)
	}
	var steps func(rule *oar.Rule, prefix string, list []oar.NextStep)
	steps = func(rule *oar.Rule, prefix string, list []oar.NextStep) {
		for i, st := range list {
			p := fmt.Sprintf("%s[%d]", prefix, i)
			if strings.TrimSpace(st.When) != "" {
				add(rule, p+".when", st.When)
			}
			steps(rule, p+".or", st.Or)
		}
	}
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		if strings.TrimSpace(rule.When) != "" {
			add(rule, "when", rule.When)
		}
		if rule.NextSteps != nil {
			steps(rule, "steps", rule.NextSteps.Steps)
		}
	}
	return out, nil
}
