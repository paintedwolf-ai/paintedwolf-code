package interactions

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"gopkg.in/yaml.v3"
)

// GuardClause captures one enclosing template or OAR predicate arm (positive or
// negated prior if/elif arm) selecting an actionable instruction site.
type GuardClause struct {
	Expression string `json:"expression"`
	Negated    bool   `json:"negated,omitempty"`
	Line       int    `json:"line,omitempty"`
}

// InstructionSite identifies one discovered actionable host instruction in the
// effective catalog along with its AST/OAR guards, include ancestry, and fact partition.
type InstructionSite struct {
	ID                string        `json:"id"`
	Family            string        `json:"family"`
	ContributionID    string        `json:"contribution_id"`
	WinnerPackID      string        `json:"winner_pack_id"`
	DeliverySite      string        `json:"delivery_site"`
	SourceSpan        string        `json:"source_span"`
	Action            string        `json:"action"`
	IncludeAncestry   []string      `json:"include_ancestry,omitempty"`
	DeliverySelectors []string      `json:"delivery_selectors,omitempty"`
	Guards            []GuardClause `json:"guards,omitempty"`
	CapabilityFacts   []string      `json:"capability_facts,omitempty"`
	OccurrenceFacts   []string      `json:"occurrence_facts,omitempty"`
	PrimitiveInputs   []string      `json:"primitive_inputs,omitempty"`
	UnresolvedFacts   []string      `json:"unresolved_facts,omitempty"`
}

type unitFrontmatter struct {
	Attaches []string `yaml:"attaches"`
}

// DiscoverCatalogInstructions enumerates effective catalog contributions using DefaultProducerGraph.
func DiscoverCatalogInstructions(
	eff *extpacks.EffectiveCatalog,
	schemas *toolschema.Config,
	names *toolvocab.Catalog,
) ([]InstructionSite, error) {
	pg, err := DefaultProducerGraph()
	if err != nil {
		return nil, err
	}
	return DiscoverCatalogInstructionsWithGraph(eff, schemas, names, pg)
}

// DiscoverCatalogInstructionsWithGraph enumerates effective catalog contributions
// (prompts, partials, units, guidance/injects/kicks, skills, and compiled OAR
// rules) using pg for capability-provider classification without rebuilding graphs.
func DiscoverCatalogInstructionsWithGraph(
	eff *extpacks.EffectiveCatalog,
	schemas *toolschema.Config,
	names *toolvocab.Catalog,
	pg *ProducerGraph,
) ([]InstructionSite, error) {
	if eff == nil {
		return nil, fmt.Errorf("nil effective catalog")
	}
	vocab, err := prompts.BuildReaderFactVocabulary(eff)
	if err != nil {
		return nil, fmt.Errorf("build reader fact vocabulary: %w", err)
	}
	reachFacts := prompts.DeclaredReachGuidanceFacts()

	var sites []InstructionSite
	seen := map[string]bool{}
	addSite := func(site InstructionSite) {
		if seen[site.ID] {
			return
		}
		seen[site.ID] = true
		sites = append(sites, site)
	}

	// Discover template contributions from eff.Loaded.
	for _, unitID := range eff.LoadedUnitIDs() {
		if _, _, archived := extpacks.SplitArchiveUnitID(unitID); archived {
			continue
		}
		rawBytes, packID, ok := eff.UnitContent(unitID)
		if !ok {
			continue
		}
		family, delivery, body, selectors, keep := classifyCatalogTemplateUnit(eff, unitID, rawBytes)
		if !keep {
			continue
		}
		walkTemplateContribution(
			eff, names, vocab, reachFacts,
			family, unitID, packID, delivery, body,
			nil, selectors, nil, map[string]bool{unitID: true},
			addSite,
		)
	}

	// Discover effective skills (SKILL.md and references/*.md).
	skillsList, _ := extpacks.LoadEffectiveSkills(eff)
	for _, sk := range skillsList {
		if sk.Project || strings.TrimSpace(sk.Metadata["authoring_reference"]) == "true" {
			continue
		}
		var sel []string
		if hr := strings.TrimSpace(sk.Metadata["host_resources"]); hr != "" {
			sel = append(sel, "host_resources:"+hr)
		}
		if anyOf := strings.TrimSpace(sk.Metadata["paintedwolf.any_of_tools"]); anyOf != "" {
			sel = append(sel, "any_of_tools:"+anyOf)
		}
		packID := "stock"
		if u, ok := eff.Loaded["skills/"+sk.Name]; ok && u.WinnerPackID != "" {
			packID = u.WinnerPackID
		}
		walkTemplateContribution(
			eff, names, vocab, reachFacts,
			"skill", "skills/"+sk.Name+"/SKILL.md", packID, "skill_body:"+sk.Name, sk.Body,
			nil, sel, nil, map[string]bool{},
			addSite,
		)
		for _, rel := range sk.MarkdownResources() {
			if resBytes, rErr := sk.ReadResource(rel); rErr == nil {
				cid := "skills/" + sk.Name + "/" + rel
				walkTemplateContribution(
					eff, names, vocab, reachFacts,
					"skill_resource", cid, packID, "skill_resource:"+sk.Name+":"+rel, string(resBytes),
					nil, sel, nil, map[string]bool{},
					addSite,
				)
			}
		}
	}

	// Discover effective compiled OAR rules and next-steps.
	oarSites, err := discoverOARRuleInstructions(eff, schemas, names)
	if err != nil {
		return nil, err
	}
	for _, s := range oarSites {
		addSite(s)
	}

	_ = pg
	slices.SortFunc(sites, func(a, b InstructionSite) int {
		return strings.Compare(a.ID, b.ID)
	})
	return sites, nil
}

func classifyCatalogTemplateUnit(eff *extpacks.EffectiveCatalog, unitID string, rawBytes []byte) (family, delivery, body string, selectors []string, keep bool) {
	if p, ok := eff.UnitPath(unitID); ok {
		ps := p.String()
		if strings.HasSuffix(ps, ".yaml") || strings.HasSuffix(ps, ".yml") || strings.HasSuffix(ps, ".json") {
			return "", "", "", nil, false
		}
	}
	clean := strings.TrimSuffix(unitID, ".md")
	switch {
	case strings.HasPrefix(clean, "agents/prompts/"):
		return "agent_prompt", "standing_persona:" + strings.TrimPrefix(clean, "agents/prompts/"), string(rawBytes), nil, true
	case strings.HasPrefix(clean, "modes/"):
		return "mode_prompt", "coordinator_mode:" + strings.TrimPrefix(clean, "modes/"), string(rawBytes), nil, true
	case strings.HasPrefix(clean, "surfaces/"):
		return "surface_prompt", "coordinator_surface:" + strings.TrimPrefix(clean, "surfaces/"), string(rawBytes), nil, true
	case strings.HasPrefix(clean, "shared/units/"):
		var ufm unitFrontmatter
		if fmRaw := extractFrontmatterYAML(string(rawBytes)); fmRaw != "" {
			_ = yaml.Unmarshal([]byte(fmRaw), &ufm)
		}
		var sel []string
		for _, att := range ufm.Attaches {
			sel = append(sel, "attaches:"+att)
		}
		return "prompt_unit", "unit_slot:" + strings.TrimPrefix(clean, "shared/units/"), string(promptunit.Body(rawBytes)), sel, true
	case strings.HasPrefix(clean, "guidance/") && !strings.HasPrefix(clean, "guidance/gate-feedback/"):
		rel := strings.TrimPrefix(clean, "guidance/")
		if prompts.IsZeroToolUtilityTemplate(rel) || prompts.IsStructuralGuidanceFragment(rel) || prompts.IsStructuralInjectFragment(rel) {
			return "", "", "", nil, false
		}
		return "guidance", "guidance:" + rel, string(rawBytes), nil, true
	default:
		return "", "", "", nil, false
	}
}

func extractFrontmatterYAML(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "---") {
		return ""
	}
	rest := trimmed[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func walkTemplateContribution(
	eff *extpacks.EffectiveCatalog,
	names *toolvocab.Catalog,
	vocab prompts.ReaderFactVocabulary,
	reachFacts map[string]bool,
	family, contribID, packID, deliverySite, body string,
	ancestry, selectors []string,
	guards []GuardClause,
	includeStack map[string]bool,
	emit func(InstructionSite),
) {
	tree, err := pongoplain.ParseTemplateTree(body)
	if err != nil || tree == nil {
		return
	}
	walkTemplateNodes(eff, names, vocab, reachFacts, family, contribID, packID, deliverySite, tree.Nodes, ancestry, selectors, guards, nil, includeStack, emit)
}

func walkTemplateNodes(
	eff *extpacks.EffectiveCatalog,
	names *toolvocab.Catalog,
	vocab prompts.ReaderFactVocabulary,
	reachFacts map[string]bool,
	family, contribID, packID, deliverySite string,
	nodes []*pongoplain.TemplateNode,
	ancestry, selectors []string,
	guards []GuardClause,
	loopVars map[string]bool,
	includeStack map[string]bool,
	emit func(InstructionSite),
) {
	for _, n := range nodes {
		if n == nil {
			continue
		}
		switch n.Kind {
		case pongoplain.NodeText:
			emitActionsFromText(names, vocab, reachFacts, family, contribID, packID, deliverySite, n.Line, n.Text, nil, ancestry, selectors, guards, loopVars, emit)
		case pongoplain.NodeOutput:
			var outRefs []string
			if n.Expr != nil {
				outRefs = n.Expr.Identifiers()
			}
			if n.Expr != nil && n.Expr.Kind == pongoplain.ExprTernary {
				condGuard := GuardClause{Expression: n.RawExpr, Line: n.Line}
				if n.Expr.Left != nil && n.Expr.Left.Kind == pongoplain.ExprLiteral {
					emitActionsFromText(names, vocab, reachFacts, family, contribID, packID, deliverySite, n.Line, n.Expr.Left.Literal, outRefs, ancestry, selectors, append(slices.Clone(guards), condGuard), loopVars, emit)
				}
				if n.Expr.Right != nil && n.Expr.Right.Kind == pongoplain.ExprLiteral {
					negGuard := GuardClause{Expression: n.RawExpr, Negated: true, Line: n.Line}
					emitActionsFromText(names, vocab, reachFacts, family, contribID, packID, deliverySite, n.Line, n.Expr.Right.Literal, outRefs, ancestry, selectors, append(slices.Clone(guards), negGuard), loopVars, emit)
				}
			} else {
				for _, ref := range outRefs {
					if isOARCallSubstitutionFact(ref) {
						capFacts, occFacts := partitionTemplateGuardFacts(vocab, reachFacts, selectors, guards, outRefs, loopVars)
						span := fmt.Sprintf("%s:L%d", contribID, n.Line)
						act := "call_substitution:" + ref
						emit(InstructionSite{
							ID:                fmt.Sprintf("%s|%s|%s|%s", deliverySite, span, strings.Join(ancestry, ">"), act),
							Family:            family,
							ContributionID:    contribID,
							WinnerPackID:      packID,
							DeliverySite:      deliverySite,
							SourceSpan:        span,
							Action:            act,
							IncludeAncestry:   slices.Clone(ancestry),
							DeliverySelectors: slices.Clone(selectors),
							Guards:            slices.Clone(guards),
							CapabilityFacts:   capFacts,
							OccurrenceFacts:   occFacts,
						})
					}
				}
			}
		case pongoplain.NodeIf:
			var priorNegated []GuardClause
			for _, arm := range n.Branches {
				armGuards := slices.Clone(guards)
				armGuards = append(armGuards, priorNegated...)
				if strings.TrimSpace(arm.RawExpr) != "" {
					armGuards = append(armGuards, GuardClause{Expression: arm.RawExpr, Line: arm.Line})
					priorNegated = append(priorNegated, GuardClause{Expression: arm.RawExpr, Negated: true, Line: arm.Line})
				}
				walkTemplateNodes(eff, names, vocab, reachFacts, family, contribID, packID, deliverySite, arm.Body, ancestry, selectors, armGuards, loopVars, includeStack, emit)
			}
		case pongoplain.NodeFor:
			forGuards := append(slices.Clone(guards), GuardClause{Expression: "nonempty(" + n.RawExpr + ")", Line: n.Line})
			nextLoops := map[string]bool{}
			for k, v := range loopVars {
				nextLoops[k] = v
			}
			for _, lv := range n.LoopVars {
				nextLoops[lv] = true
			}
			walkTemplateNodes(eff, names, vocab, reachFacts, family, contribID, packID, deliverySite, n.Body, ancestry, selectors, forGuards, nextLoops, includeStack, emit)
			if len(n.EmptyBody) > 0 {
				emptyGuards := append(slices.Clone(guards), GuardClause{Expression: "nonempty(" + n.RawExpr + ")", Negated: true, Line: n.Line})
				walkTemplateNodes(eff, names, vocab, reachFacts, family, contribID, packID, deliverySite, n.EmptyBody, ancestry, selectors, emptyGuards, loopVars, includeStack, emit)
			}
		case pongoplain.NodeWith:
			walkTemplateNodes(eff, names, vocab, reachFacts, family, contribID, packID, deliverySite, n.Body, ancestry, selectors, guards, loopVars, includeStack, emit)
		case pongoplain.NodeInclude:
			incID, incUnit, ok := prompts.ResolveCatalogTemplateUnit(eff, n.IncludeRef)
			if !ok || includeStack[incID] {
				continue
			}
			nextStack := map[string]bool{}
			for k, v := range includeStack {
				nextStack[k] = v
			}
			nextStack[incID] = true
			nextAncestry := append(slices.Clone(ancestry), contribID+fmt.Sprintf(":L%d->%s", n.Line, incID))
			incPack := packID
			if incUnit.WinnerPackID != "" {
				incPack = incUnit.WinnerPackID
			}
			walkTemplateContribution(eff, names, vocab, reachFacts, family, incID, incPack, deliverySite, string(incUnit.Content), nextAncestry, selectors, guards, nextStack, emit)
		}
	}
}

func emitActionsFromText(
	names *toolvocab.Catalog,
	vocab prompts.ReaderFactVocabulary,
	reachFacts map[string]bool,
	family, contribID, packID, deliverySite string,
	line int,
	text string,
	extraRefs []string,
	ancestry, selectors []string,
	guards []GuardClause,
	loopVars map[string]bool,
	emit func(InstructionSite),
) {
	if names == nil || strings.TrimSpace(text) == "" {
		return
	}
	actions := map[string]bool{}
	for _, ex := range toolvocabtest.ParseCallExamples(names, text) {
		if ex.Tool != "" {
			action := ex.Tool
			if ex.Tool == "request_tools" {
				if need, ok := ex.Args["need"].(string); ok && need != "" {
					action = "request_tools(need=" + need + ")"
				}
			} else if ex.Tool == "wait" && strings.Contains(ex.Raw, "process_done") {
				action = "wait(process_done)"
			}
			actions[action] = true
		}
	}
	for _, t := range toolvocab.ToolsIn(names, text) {
		if names.KnownName(t) && !toolcontract.CommandFormTool(t) && !actions[t] {
			actions[t] = true
		}
	}
	if len(actions) == 0 {
		return
	}

	capFacts, occFacts := partitionTemplateGuardFacts(vocab, reachFacts, selectors, guards, extraRefs, loopVars)
	sortedActions := make([]string, 0, len(actions))
	for a := range actions {
		sortedActions = append(sortedActions, a)
	}
	slices.Sort(sortedActions)
	span := fmt.Sprintf("%s:L%d", contribID, line)
	for _, act := range sortedActions {
		id := fmt.Sprintf("%s|%s|%s|%s", deliverySite, span, strings.Join(ancestry, ">"), act)
		emit(InstructionSite{
			ID:                id,
			Family:            family,
			ContributionID:    contribID,
			WinnerPackID:      packID,
			DeliverySite:      deliverySite,
			SourceSpan:        span,
			Action:            act,
			IncludeAncestry:   slices.Clone(ancestry),
			DeliverySelectors: slices.Clone(selectors),
			Guards:            slices.Clone(guards),
			CapabilityFacts:   capFacts,
			OccurrenceFacts:   occFacts,
		})
	}
}

func partitionTemplateGuardFacts(
	vocab prompts.ReaderFactVocabulary,
	reachFacts map[string]bool,
	selectors []string,
	guards []GuardClause,
	extraRefs []string,
	loopVars map[string]bool,
) (capFacts, occFacts []string) {
	caps := map[string]bool{}
	occs := map[string]bool{}
	for _, sel := range selectors {
		if att, ok := strings.CutPrefix(sel, "attaches:"); ok {
			if strings.HasPrefix(att, "command_") {
				caps["profile_"+att] = true
			} else {
				caps["profile_has_"+att] = true
			}
		}
	}
	addRef := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" || ref == "true" || ref == "false" || ref == "nonempty" || ref == "forloop" || strings.HasPrefix(ref, "forloop.") {
			return
		}
		root := ref
		if idx := strings.IndexByte(ref, '.'); idx >= 0 {
			root = ref[:idx]
		}
		if loopVars[root] {
			return
		}
		if prompts.IsDeclaredOrReservedReaderFact(ref) || prompts.IsDeclaredOrReservedReaderFact(root) ||
			vocab.AllowsReference(ref) || reachFacts[root] || ref == "wait_process_done_allowed" ||
			(strings.HasPrefix(ref, "paintedwolf.") && isOARCapabilityFact(strings.TrimPrefix(ref, "paintedwolf."))) {
			caps[ref] = true
		} else {
			occs[ref] = true
		}
	}
	for _, g := range guards {
		clean := g.Expression
		if inner, ok := strings.CutPrefix(clean, "nonempty("); ok {
			clean = strings.TrimSuffix(inner, ")")
		}
		if expr, err := pongoplain.ParseExpression(clean); err == nil && expr != nil {
			for _, id := range expr.Identifiers() {
				addRef(id)
			}
		} else {
			addRef(clean)
		}
	}
	for _, r := range extraRefs {
		addRef(r)
	}
	return sortedKeysOf(caps), sortedKeysOf(occs)
}

func discoverOARRuleInstructions(
	eff *extpacks.EffectiveCatalog,
	schemas *toolschema.Config,
	names *toolvocab.Catalog,
) ([]InstructionSite, error) {
	moduleRoot := configlayout.FindModuleRoot()
	repoRoot := filepath.Dir(moduleRoot)
	schemaDir := filepath.Join(repoRoot, "schemas")
	if err := anchorcatalog.InstallBundled(); err != nil {
		return nil, err
	}
	if err := oar.InstallEngineDeclarationBundled(schemaDir); err != nil {
		return nil, err
	}
	loader, err := oar.NewLoader(schemaDir)
	if err != nil {
		return nil, err
	}
	loader.SetToolSchemas(schemas)
	loader.SetDetectors(oar.HostDetectors(nil))
	rs, err := loader.LoadEffectivePolicyWithCatalog(eff)
	if err != nil {
		return nil, err
	}
	vocab, _ := prompts.BuildReaderFactVocabulary(eff)
	reachFacts := prompts.DeclaredReachGuidanceFacts()

	var sites []InstructionSite
	for _, rule := range rs.All() {
		if rule == nil {
			continue
		}
		packID := "stock"
		if u, ok := eff.Loaded["policy/"+rule.ID]; ok && u.WinnerPackID != "" {
			packID = u.WinnerPackID
		}
		var selectors []string
		selectors = append(selectors, "anchor:"+rule.Anchor)
		for _, t := range rule.Tools() {
			selectors = append(selectors, "selector.tool:"+t)
		}
		var baseGuards []GuardClause
		if strings.TrimSpace(rule.When) != "" {
			baseGuards = append(baseGuards, GuardClause{Expression: rule.When})
		}
		if rule.NextSteps != nil {
			walkOARSteps(rule, packID, selectors, baseGuards, "steps", rule.NextSteps.Steps, &sites)
		}
		if strings.TrimSpace(rule.Copy.Fix) != "" {
			contribID := "policy/" + rule.ID + "#fix"
			delivery := "oar:" + rule.Anchor + ":" + rule.ID
			walkTemplateContribution(
				eff, names, vocab, reachFacts,
				"oar_copy_fix", contribID, packID, delivery, rule.Copy.Fix,
				nil, selectors, baseGuards, map[string]bool{},
				func(s InstructionSite) { sites = append(sites, s) },
			)
		}
	}
	return sites, nil
}

func walkOARSteps(
	rule *oar.Rule,
	packID string,
	selectors []string,
	parentGuards []GuardClause,
	prefix string,
	steps []oar.NextStep,
	out *[]InstructionSite,
) {
	for i, st := range steps {
		stepPath := fmt.Sprintf("%s[%d]", prefix, i)
		guards := slices.Clone(parentGuards)
		if strings.TrimSpace(st.When) != "" {
			guards = append(guards, GuardClause{Expression: st.When})
		}
		action := st.Tool
		if len(st.Program) > 0 {
			action = "command:" + st.Program[0]
		}
		if st.Tool == "request_tools" {
			if need, ok := st.Args["need"].(string); ok && need != "" {
				action = "request_tools(need=" + need + ")"
			}
		}
		if action != "" {
			caps, occs := partitionOARFacts(rule, st, guards)
			contribID := "policy/" + rule.ID + "#next-steps"
			span := fmt.Sprintf("%s:%s", contribID, stepPath)
			delivery := "oar:" + rule.Anchor + ":" + rule.ID
			*out = append(*out, InstructionSite{
				ID:                fmt.Sprintf("%s|%s||%s", delivery, span, action),
				Family:            "oar_next_step",
				ContributionID:    contribID,
				WinnerPackID:      packID,
				DeliverySite:      delivery,
				SourceSpan:        span,
				Action:            action,
				DeliverySelectors: slices.Clone(selectors),
				Guards:            guards,
				CapabilityFacts:   caps,
				OccurrenceFacts:   occs,
			})
		}
		walkOARSteps(rule, packID, selectors, guards, stepPath+".or", st.Or, out)
	}
}

func partitionOARFacts(rule *oar.Rule, st oar.NextStep, guards []GuardClause) (capFacts, occFacts []string) {
	caps := map[string]bool{}
	occs := map[string]bool{}
	if st.Tool != "" {
		caps["reach.status:"+st.Tool] = true
	}
	if len(st.Program) > 0 {
		caps["reach.program:"+st.Program[0]] = true
	}
	for _, g := range guards {
		refs, err := oarcore.ConditionReferences(g.Expression)
		if err != nil {
			continue
		}
		for _, r := range refs {
			short := strings.TrimPrefix(r, "paintedwolf.")
			if isOARCapabilityFact(short) {
				caps[r] = true
			} else if strings.HasPrefix(r, "paintedwolf.") || r == "tool" {
				occs[r] = true
			}
		}
	}
	_ = rule
	return sortedKeysOf(caps), sortedKeysOf(occs)
}

func isOARCapabilityFact(short string) bool {
	if slices.Contains(oar.CanCallFacts(), short) {
		return true
	}
	if pg, err := DefaultProducerGraph(); err == nil && pg.IsCapabilityProviderFact(short) {
		return true
	}
	switch short {
	case "session_mode", "session_posture", "surface", "session_role", "worker_leg", "profile_mutation_capable":
		return true
	default:
		return false
	}
}

func isOARCallSubstitutionFact(ref string) bool {
	if !strings.HasPrefix(ref, "paintedwolf.") {
		return false
	}
	pg, err := DefaultProducerGraph()
	if err != nil || !pg.IsCapabilityProviderFact(ref) {
		return false
	}
	tr := pg.TraceFact(ref)
	for _, inp := range tr.PrimitiveInputs {
		if inp == "toolreach.QuoteProgram" || inp == "toolreach.Load" || inp == "toolreach.Text" || inp == "Call.Tool" {
			return true
		}
	}
	return false
}

func sortedKeysOf(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
