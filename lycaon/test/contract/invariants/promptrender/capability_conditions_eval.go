package promptrender

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/test/contract/invariants"
	"gopkg.in/yaml.v3"
)

func directTextOf(nodes []*pongoplain.TemplateNode) string {
	var b strings.Builder
	for _, n := range nodes {
		if n.Kind == pongoplain.NodeText {
			b.WriteString(n.Text)
		}
	}
	return b.String()
}

func stripNegatedClauses(text string) string {
	return negatedClauseRe.ReplaceAllString(text, " ")
}

func surfaceFloorSeed(rel string, surfacePlans map[string][]string, schemas *toolschema.Config) map[string]bool {
	out := map[string]bool{}
	const prefix = "coordinator-mode-"
	base := path.Base(rel)
	if !strings.HasPrefix(base, prefix) {
		return out
	}
	surfaceID := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(base, prefix), ".md"), "-", "_")
	for _, t := range surfacePlans[surfaceID] {
		out["profile_has_"+t] = true
		markImpliedCapabilities("profile_has_"+t, schemas, out)
	}
	return out
}

type capabilityChecker struct {
	run             *invariants.Run
	stock           *Stock
	vocab           prompts.ReaderFactVocabulary
	includes        map[string]*classifiedSource
	includedTargets map[string]bool
	reported        map[string]bool
}

func (cc *capabilityChecker) emit(msg string) {
	if !cc.reported[msg] {
		cc.reported[msg] = true
		cc.run.Report("%s", msg)
	}
}

func isAuthoringMetaDoc(rel string) bool {
	v, err := toolvocabtest.StockBacktickVocab()
	return err == nil && v.IsAuthoringReferenceSource(rel)
}

// buildSkillGuaranteedMap derives facts guaranteed by toolvocab.SkillToolsOffered
// and each unconditionally required tool's minimum production projected schema.
func buildSkillGuaranteedMap(s *Stock, skillBodies map[string]*classifiedSource) map[string]map[string]bool {
	hostResCommonFields := intersectProjectedFields(s.Schemas, []struct {
		name     string
		readOnly bool
	}{
		{"command", false},
		{"terminal_open", false},
		{"verify", false},
	})

	out := map[string]map[string]bool{}
	for key, sb := range skillBodies {
		g := map[string]bool{}
		if sb.hostResource {
			g["runner_capability_request_tools"] = true
			g["runner_host_resources_tools"] = true
			for f := range hostResCommonFields {
				g["schema_accepts:"+f] = true
			}
		}
		if sb.tree != nil {
			topText := directTextOf(sb.tree.Nodes)
			for _, t := range toolvocab.ToolsIn(s.Names, topText) {
				if slices.Contains(sb.anyOfTools, t) || !s.Names.KnownName(t) || toolcontract.CommandFormTool(t) {
					continue
				}
				g["profile_has_"+t] = true
				markImpliedCapabilities("profile_has_"+t, s.Schemas, g)
			}
		}
		out[key] = g
	}
	return out
}

func intersectProjectedFields(schemas *toolschema.Config, candidates []struct {
	name     string
	readOnly bool
}) map[string]bool {
	if schemas == nil || len(candidates) == 0 {
		return nil
	}
	var common map[string]bool
	for i, c := range candidates {
		proj, ok := minimumProductionProjectedSchema(c.name, c.readOnly, schemas)
		if !ok {
			return nil
		}
		cur := map[string]bool{}
		for _, p := range toolschema.ArgFieldPaths(proj) {
			cur[p] = true
		}
		if i == 0 {
			common = cur
			continue
		}
		for k := range common {
			if !cur[k] {
				delete(common, k)
			}
		}
	}
	return common
}

// SkillGuaranteedSchemaFieldsForText returns the schema_accepts:<field> set
// retained by buildSkillGuaranteedMap for rawSkillMarkdown.
func SkillGuaranteedSchemaFieldsForText(s *Stock, rawSkillMarkdown string) ([]string, error) {
	fmRaw, body := splitMarkdownFrontmatter(rawSkillMarkdown)
	cs := &classifiedSource{family: "skills/SKILL.md", packKey: "test", skillName: "test", body: body}
	if fmRaw != "" {
		var sfm skillFrontmatter
		if err := yaml.Unmarshal([]byte(fmRaw), &sfm); err != nil {
			return nil, err
		}
		if strings.TrimSpace(sfm.Metadata["host_resources"]) != "" {
			cs.hostResource = true
		}
		for _, part := range strings.Split(sfm.Metadata["paintedwolf.any_of_tools"], ",") {
			if t := strings.TrimSpace(part); t != "" {
				cs.anyOfTools = append(cs.anyOfTools, t)
			}
		}
	}
	tree, err := pongoplain.ParseTemplateTree(cs.body)
	if err != nil {
		return nil, err
	}
	cs.tree = tree
	gmap := buildSkillGuaranteedMap(s, map[string]*classifiedSource{"test:test": cs})["test:test"]
	var fields []string
	for k, ok := range gmap {
		if ok && strings.HasPrefix(k, "schema_accepts:") {
			fields = append(fields, strings.TrimPrefix(k, "schema_accepts:"))
		}
	}
	slices.Sort(fields)
	return fields, nil
}

func (cc *capabilityChecker) checkBinding(sourceLabel string, line int, tag string, b pongoplain.Binding, aliases map[string]localAlias) {
	if prompts.IsDeclaredOrReservedReaderFact(b.Name) || strings.HasPrefix(b.Name, "can_") || strings.HasPrefix(b.Name, "has_") {
		aliases[b.Name] = localAlias{name: b.Name, rawExpr: b.RawExpr, isCapAlias: true}
		cc.emit(fmt.Sprintf("%s:%d: expression \"%s %s = %s\" defines forbidden capability fact/prefix binding %q",
			sourceLabel, line, tag, b.Name, b.RawExpr, b.Name))
		return
	}
	refsFact := false
	refsBool := false
	hasOps := b.Expr.HasLogicalOrCompare()
	for _, id := range b.Expr.Identifiers() {
		root := id
		if dot := strings.IndexByte(id, '.'); dot >= 0 {
			root = id[:dot]
		}
		if prev, ok := aliases[root]; ok {
			refsFact = true
			if prev.isCapAlias {
				refsBool = true
			}
		}
		if prompts.IsDeclaredOrReservedReaderFact(id) || prompts.IsDeclaredOrReservedReaderFact(root) || cc.vocab.AllowsReference(id) || workspaceIdentityOnlyFacts[root] {
			refsFact = true
			if isBoolCapabilityFact(cc.vocab, id) || isBoolCapabilityFact(cc.vocab, root) {
				refsBool = true
			}
		}
	}
	isCap := (hasOps && refsFact) || refsBool
	if refsFact {
		aliases[b.Name] = localAlias{name: b.Name, rawExpr: b.RawExpr, isCapAlias: isCap}
	}
	if isCap {
		cc.emit(fmt.Sprintf("%s:%d: expression \"%s %s = %s\" defines local capability alias/derivation %q; declare the fact at the host reader boundary",
			sourceLabel, line, tag, b.Name, b.RawExpr, b.Name))
	}
}

func (cc *capabilityChecker) inspectSegmentActions(
	sourceLabel string,
	line int,
	condExpr string,
	ownCondExpr *pongoplain.Expr,
	ownWorkspaceOnly bool,
	skipStandalonePartialUnconditional bool,
	established map[string]bool,
	aliases map[string]localAlias,
	segText string,
	authoringDoc bool,
) {
	positiveText := stripNegatedClauses(segText)
	if strings.TrimSpace(positiveText) == "" {
		return
	}
	if ownCondExpr != nil {
		for _, id := range ownCondExpr.Identifiers() {
			root := id
			if dot := strings.IndexByte(id, '.'); dot >= 0 {
				root = id[:dot]
			}
			if al, ok := aliases[root]; ok && al.isCapAlias {
				cc.emit(fmt.Sprintf("%s:%d: expression %q uses local alias %q (%s) to gate action prose",
					sourceLabel, line, condExpr, al.name, al.rawExpr))
			}
		}
	}
	if comparesIdent(ownCondExpr, "tool_profile") {
		cc.emit(fmt.Sprintf("%s:%d: expression %q checks static tool_profile identity instead of declared turn capability facts",
			sourceLabel, line, condExpr))
	}
	if isMultiRunnerDisjunction(ownCondExpr) {
		for _, m := range prunedSchemaClaimRe.FindAllStringSubmatch(positiveText, -1) {
			arg := m[1]
			if strings.HasPrefix(arg, "capability_request") || strings.HasPrefix(arg, "secret_use") || strings.HasPrefix(arg, "socks_proxy") {
				cc.emit(fmt.Sprintf("%s:%d: expression %q reconstructs projected-schema argument %q via inline runner disjunction; consume declared runner_*_tools projection",
					sourceLabel, line, condExpr, arg))
			}
		}
	}
	if authoringDoc || skipStandalonePartialUnconditional {
		return
	}
	cc.checkActionReachInSegment(sourceLabel, line, condExpr, ownWorkspaceOnly, segText, positiveText, established)
}

func (cc *capabilityChecker) checkActionReachInSegment(
	sourceLabel string,
	line int,
	condExpr string,
	ownWorkspaceOnly bool,
	rawText string,
	positiveText string,
	established map[string]bool,
) {
	exprDesc := condExpr
	if exprDesc == "" {
		exprDesc = "<unguarded>"
	}
	if externalSelectorRe.MatchString(rawText) {
		hasSelector := established["can_command_external_selector"] ||
			(established["profile_command_external_exec"] && established["command_external_shown"])
		if !hasSelector {
			cc.emit(fmt.Sprintf("%s:%d: expression %q promises action \"external --\" without selector visibility fact can_command_external_selector",
				sourceLabel, line, exprDesc))
		}
	}
	if curlInCommandRe.MatchString(positiveText) && !established["profile_command_network_egress"] {
		cc.emit(fmt.Sprintf("%s:%d: expression %q promises action \"curl\" without profile_command_network_egress",
			sourceLabel, line, exprDesc))
	}
	for _, gm := range gitCmdActionRe.FindAllStringSubmatch(positiveText, -1) {
		sub := gm[1]
		switch sub {
		case "add", "commit":
			if !established["git_commit_reach"] {
				cc.emit(fmt.Sprintf("%s:%d: expression %q promises action \"git %s\" without declared capability fact can_git_commit",
					sourceLabel, line, exprDesc, sub))
			}
		case "bisect":
			if !established["git_bisect_reach"] {
				cc.emit(fmt.Sprintf("%s:%d: expression %q promises action \"git %s\" without declared capability fact can_git_bisect",
					sourceLabel, line, exprDesc, sub))
			}
		case "worktree":
			if !established["git_bisect_reach"] && !established["git_commit_reach"] {
				cc.emit(fmt.Sprintf("%s:%d: expression %q promises action \"git %s\" without declared capability fact can_git_bisect",
					sourceLabel, line, exprDesc, sub))
			}
		default:
			if ownWorkspaceOnly && !established["git_read_reach"] && !established["profile_has_command"] {
				cc.emit(fmt.Sprintf("%s:%d: expression %q promises action \"git %s\" without declared capability fact can_git_read",
					sourceLabel, line, exprDesc, sub))
			}
		}
	}
	for _, pm := range prunedSchemaClaimRe.FindAllStringSubmatch(positiveText, -1) {
		arg := pm[1]
		if !schemaClaimEstablished(arg, established) {
			cc.emit(fmt.Sprintf("%s:%d: expression %q promises projected-schema argument %q without declared runner/schema capability fact",
				sourceLabel, line, exprDesc, arg))
		}
	}
	if ownWorkspaceOnly {
		for _, tool := range toolvocab.ToolsIn(cc.stock.Names, positiveText) {
			if established["profile_has_"+tool] ||
				(tool == "request_tools" && (established["more_tools_loadable"] || hasPrefixFact(established, "profile_can_request.") || hasPrefixFact(established, "can_request_"))) ||
				(tool == "command" && established["profile_has_command"]) {
				continue
			}
			cc.emit(fmt.Sprintf("%s:%d: expression %q promises tool %q using only workspace/identity facts (missing profile_has_%s or declared capability fact)",
				sourceLabel, line, exprDesc, tool, tool))
		}
	}
}

func schemaClaimEstablished(arg string, established map[string]bool) bool {
	if strings.HasPrefix(arg, "command(") && strings.Contains(arg, "verification:") {
		return established["can_command_verify"] || established["profile_command_external_exec"] || established["schema_accepts:verification"]
	}
	if strings.HasPrefix(arg, "response_type:") {
		return established["can_ask_user_secret"]
	}
	clean := strings.TrimSpace(strings.TrimSuffix(arg, ": true"))
	clean = strings.TrimSpace(strings.TrimSuffix(clean, ":true"))
	if established["schema_accepts:"+clean] {
		return true
	}
	switch {
	case clean == "terminal_capture":
		return established["profile_has_terminal_capture"] || established["profile_command_external_exec"]
	case clean == "snapshot_capture":
		return established["profile_command_external_exec"]
	case strings.HasPrefix(clean, "socks_proxy"):
		return established["runner_socks_proxy_tools"]
	case strings.HasPrefix(clean, "secret_use"):
		return established["runner_secret_use_tools"]
	case clean == "capability_request":
		return established["runner_capability_request_tools"]
	default:
		return false
	}
}

func (cc *capabilityChecker) checkNodes(
	cs *classifiedSource,
	originSuffix string,
	nodes []*pongoplain.TemplateNode,
	baseFacts map[string]bool,
	guards []activeGuard,
	aliases map[string]localAlias,
	includeStack map[string]bool,
) {
	sourceLabel := cs.rel + originSuffix
	authoringDoc := isAuthoringMetaDoc(cs.rel)

	established := cloneBoolMap(baseFacts)
	negatedFacts := map[string]bool{}
	for _, g := range guards {
		if g.negated {
			collectPositiveFacts(g.expr, true, negatedFacts, cc.stock.Schemas, established)
		}
	}
	for _, g := range guards {
		if !g.negated {
			collectPositiveFacts(g.expr, false, negatedFacts, cc.stock.Schemas, established)
		}
	}

	var topCondRaw string
	var topCondExpr *pongoplain.Expr
	ownWorkspaceOnly := false
	if len(guards) > 0 {
		last := guards[len(guards)-1]
		topCondRaw = last.rawExpr
		topCondExpr = last.expr
		ids := last.expr.Identifiers()
		ownWorkspaceOnly = len(ids) > 0 && !slices.ContainsFunc(ids, func(id string) bool {
			if al, ok := aliases[id]; ok && al.isCapAlias {
				return true
			}
			return !workspaceIdentityOnlyFacts[id]
		})
	}

	skipStandalonePartialUnconditional := originSuffix == "" &&
		(cs.family == "shared/partials" || cs.family == "shared/archetypes") &&
		cc.includedTargets[cs.rel] &&
		len(guards) == 0

	for _, n := range nodes {
		switch n.Kind {
		case pongoplain.NodeText:
			cc.inspectSegmentActions(
				sourceLabel, n.Line, topCondRaw, topCondExpr,
				ownWorkspaceOnly, skipStandalonePartialUnconditional,
				established, aliases, n.Text, authoringDoc,
			)
		case pongoplain.NodeOutput:
			cc.checkTernaryOutput(sourceLabel, n, established, aliases, authoringDoc)
		case pongoplain.NodeSet:
			for _, b := range n.Bindings {
				cc.checkBinding(sourceLabel, n.Line, "set", b, aliases)
			}
		case pongoplain.NodeWith:
			scopedAliases := cloneAliasMap(aliases)
			for _, b := range n.Bindings {
				cc.checkBinding(sourceLabel, n.Line, "with", b, scopedAliases)
			}
			cc.checkNodes(cs, originSuffix, n.Body, baseFacts, guards, scopedAliases, includeStack)
		case pongoplain.NodeFor:
			cc.checkNodes(cs, originSuffix, n.Body, baseFacts, guards, aliases, includeStack)
			cc.checkNodes(cs, originSuffix, n.EmptyBody, baseFacts, guards, aliases, includeStack)
		case pongoplain.NodeInclude:
			scopedAliases := cloneAliasMap(aliases)
			for _, b := range n.Bindings {
				cc.checkBinding(sourceLabel, n.Line, "include", b, scopedAliases)
			}
			if target, ok := cc.includes[n.IncludeRef]; ok && target.tree != nil && !includeStack[target.rel] {
				nextInclude := cloneBoolMap(includeStack)
				nextInclude[target.rel] = true
				cc.checkNodes(target, fmt.Sprintf(" (included from %s:%d)", cs.rel, n.Line), target.tree.Nodes, established, guards, scopedAliases, nextInclude)
			}
		case pongoplain.NodeIf:
			var negatedPrior []activeGuard
			for _, br := range n.Branches {
				branchGuards := append(slices.Clone(guards), negatedPrior...)
				if br.Expr != nil {
					branchGuards = append(branchGuards, activeGuard{rawExpr: br.RawExpr, expr: br.Expr})
				} else if len(negatedPrior) > 0 {
					lastNeg := negatedPrior[len(negatedPrior)-1]
					branchGuards[len(branchGuards)-1].rawExpr = "else of (" + lastNeg.rawExpr + ")"
				}
				cc.checkNodes(cs, originSuffix, br.Body, baseFacts, branchGuards, cloneAliasMap(aliases), includeStack)
				if br.Expr != nil {
					negatedPrior = append(negatedPrior, activeGuard{rawExpr: br.RawExpr, expr: br.Expr, negated: true})
				}
			}
		}
	}
}

func (cc *capabilityChecker) checkTernaryOutput(
	sourceLabel string,
	n *pongoplain.TemplateNode,
	established map[string]bool,
	aliases map[string]localAlias,
	authoringDoc bool,
) {
	if n.Expr == nil || n.Expr.Kind != pongoplain.ExprTernary {
		return
	}
	ids := n.Expr.Cond.Identifiers()
	wsOnly := len(ids) > 0 && !slices.ContainsFunc(ids, func(id string) bool { return !workspaceIdentityOnlyFacts[id] })
	posEst := cloneBoolMap(established)
	collectPositiveFacts(n.Expr.Cond, false, map[string]bool{}, cc.stock.Schemas, posEst)
	if n.Expr.Left != nil && n.Expr.Left.Kind == pongoplain.ExprLiteral {
		cc.inspectSegmentActions(sourceLabel, n.Line, n.RawExpr, n.Expr.Cond, wsOnly, false, posEst, aliases, n.Expr.Left.Literal, authoringDoc)
	}
	negEst := cloneBoolMap(established)
	collectPositiveFacts(n.Expr.Cond, true, map[string]bool{}, cc.stock.Schemas, negEst)
	if n.Expr.Right != nil && n.Expr.Right.Kind == pongoplain.ExprLiteral {
		cc.inspectSegmentActions(sourceLabel, n.Line, n.RawExpr, n.Expr.Cond, wsOnly, false, negEst, aliases, n.Expr.Right.Literal, authoringDoc)
	}
}

// Every shipped prompt, partial, unit, guidance template, skill body, and skill
// reference expresses action/tool/schema eligibility through declared host
// reader facts rather than local aliases, profile-ID checks, inline runner
// disjunctions, self-justifying body mentions, or workspace-metadata-only conditions.
func capabilityConditionsUseDeclaredFacts(r *invariants.Run) {
	parsed, includes, skillBodies := classifyAndParseSources(r)
	if len(parsed) == 0 {
		return
	}
	s := stock(r)
	vocab, err := prompts.BuildReaderFactVocabulary(s.Catalog)
	r.Must(err)

	surfacePlans := map[string][]string{}
	if plans, pErr := surface.CompileToolPlans(1); pErr == nil {
		for id, pl := range plans {
			surfacePlans[id] = pl.ImmediateNames()
		}
	}
	includedTargets := collectIncludedTargets(parsed, includes)
	skillGuaranteed := buildSkillGuaranteedMap(s, skillBodies)
	cc := &capabilityChecker{
		run:             r,
		stock:           s,
		vocab:           vocab,
		includes:        includes,
		includedTargets: includedTargets,
		reported:        map[string]bool{},
	}

	for _, cs := range parsed {
		if cs.tree == nil {
			continue
		}
		seed := buildSourceSeed(cs, surfacePlans, skillGuaranteed, s.Schemas)
		cc.checkNodes(cs, "", cs.tree.Nodes, seed, nil, map[string]localAlias{}, map[string]bool{cs.rel: true})
	}
}

func collectIncludedTargets(parsed []*classifiedSource, includes map[string]*classifiedSource) map[string]bool {
	out := map[string]bool{}
	var walk func([]*pongoplain.TemplateNode)
	walk = func(nodes []*pongoplain.TemplateNode) {
		for _, n := range nodes {
			if n.Kind == pongoplain.NodeInclude {
				if target, ok := includes[n.IncludeRef]; ok {
					out[target.rel] = true
				}
			}
			for _, br := range n.Branches {
				walk(br.Body)
			}
			walk(n.Body)
			walk(n.EmptyBody)
		}
	}
	for _, cs := range parsed {
		if cs.tree != nil {
			walk(cs.tree.Nodes)
		}
	}
	return out
}

func buildSourceSeed(
	cs *classifiedSource,
	surfacePlans map[string][]string,
	skillGuaranteed map[string]map[string]bool,
	schemas *toolschema.Config,
) map[string]bool {
	seed := surfaceFloorSeed(cs.rel, surfacePlans, schemas)
	for _, att := range cs.attaches {
		att = strings.TrimSpace(att)
		switch att {
		case "command_mutation":
			seed["profile_has_command"] = true
			seed["profile_command_mutation"] = true
			markImpliedCapabilities("profile_command_mutation", schemas, seed)
		case "command_external_exec":
			seed["profile_has_command"] = true
			seed["profile_command_external_exec"] = true
			markImpliedCapabilities("profile_command_external_exec", schemas, seed)
		case "command_network_egress":
			seed["profile_has_command"] = true
			seed["profile_command_network_egress"] = true
			markImpliedCapabilities("profile_command_network_egress", schemas, seed)
		default:
			if att != "" {
				seed["profile_has_"+att] = true
				markImpliedCapabilities("profile_has_"+att, schemas, seed)
			}
		}
	}
	if cs.family == "skills/SKILL.md" || cs.family == "skills/references" {
		parts := strings.Split(strings.TrimPrefix(cs.rel, "config/packs/"), "/skills/")
		if len(parts) == 2 {
			dirName := strings.SplitN(parts[1], "/", 2)[0]
			for k, v := range skillGuaranteed[cs.packKey+":"+dirName] {
				if v {
					seed[k] = true
				}
			}
		}
	}
	return seed
}

func hasPrefixFact(m map[string]bool, prefix string) bool {
	for k, v := range m {
		if v && strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

func cloneBoolMap(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneAliasMap(in map[string]localAlias) map[string]localAlias {
	out := make(map[string]localAlias, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
