package promptrender

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/toolvocab"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/test/contract/invariants"
	"gopkg.in/yaml.v3"
)

var capabilityConditionInvariants = []invariants.Invariant{
	{
		Name:    "stock-sources-reconciled-and-parsed",
		Anchors: []invariants.Anchor{invariants.Func("internal/prompts", "CheckTemplateVocabulary")},
		Check:   stockSourcesReconciledAndParsed,
	},
	{
		Name:    "capability-conditions-use-declared-facts",
		Anchors: []invariants.Anchor{invariants.Func("internal/prompts", "CheckTemplateVocabulary"), personaRenderer},
		Check:   capabilityConditionsUseDeclaredFacts,
	},
}

type classifiedSource struct {
	rel          string
	family       string
	packKey      string
	skillName    string
	body         string
	attaches     []string
	anyOfTools   []string
	hostResource bool
	tree         *pongoplain.TemplateTree
}

var (
	packAgentPromptRe   = regexp.MustCompile(`^packs/([^/]+/[^/]+)/agents/prompts/([^/]+\.md)$`)
	packPartialRe       = regexp.MustCompile(`^packs/([^/]+/[^/]+)/shared/partials/(.+\.md)$`)
	packUnitRe          = regexp.MustCompile(`^packs/([^/]+/[^/]+)/shared/units/[^/]+\.md$`)
	packArchetypeRe     = regexp.MustCompile(`^packs/([^/]+/[^/]+)/shared/archetypes/(.+\.md)$`)
	packGuidanceRe      = regexp.MustCompile(`^packs/([^/]+/[^/]+)/guidance/.+\.md$`)
	packArchiveGuidRe   = regexp.MustCompile(`^packs/([^/]+/[^/]+)/archive/[^/]+/[^/]+/guidance/[^/]+\.md$`)
	packSkillBodyRe     = regexp.MustCompile(`^packs/([^/]+/[^/]+)/skills/([^/]+)/SKILL\.md$`)
	packSkillRefRe      = regexp.MustCompile(`^packs/([^/]+/[^/]+)/skills/([^/]+)/references/[^/]+\.md$`)
	scannerDocExcludeRe = regexp.MustCompile(`^runtime/scanners/rules/.+/(?:README|CONTRIBUTING)\.md$`)
)

var closedDocExclusions = map[string]bool{
	"README.md":                   true,
	"oar-portable-pack/README.md": true,
	"packs/painted-wolf/platform/host/client-notices/README.md":  true,
	"packs/painted-wolf/security/host/detection-packs/README.md": true,
}

type unitFrontmatter struct {
	Attaches []string `yaml:"attaches"`
}

type skillFrontmatter struct {
	Name     string            `yaml:"name"`
	Metadata map[string]string `yaml:"metadata"`
}

func splitMarkdownFrontmatter(raw string) (front, body string) {
	if !strings.HasPrefix(raw, "---\n") && !strings.HasPrefix(raw, "---\r\n") {
		return "", raw
	}
	rest := raw[4:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", raw
	}
	front = rest[:idx]
	after := rest[idx+4:]
	if nl := strings.IndexByte(after, '\n'); nl >= 0 {
		body = after[nl+1:]
	}
	return front, body
}

func classifyAndParseSources(r *invariants.Run) (
	parsed []*classifiedSource,
	includes map[string]*classifiedSource,
	skillBodies map[string]*classifiedSource,
) {
	files := r.Tree("lycaon/config", ".md")
	r.Expect(len(files), "config markdown files")

	includes = map[string]*classifiedSource{}
	skillBodies = map[string]*classifiedSource{}

	for _, f := range files {
		rel := f.Rel
		srcLabel := "config/" + rel
		raw := f.Text()

		if closedDocExclusions[rel] || scannerDocExcludeRe.MatchString(rel) {
			if strings.Contains(raw, "{{") || strings.Contains(raw, "{%") {
				r.Report("%s: excluded static documentation contains Pongo template syntax", srcLabel)
			}
			continue
		}
		if strings.HasPrefix(rel, "fixtures/") {
			continue
		}

		cs := &classifiedSource{rel: srcLabel, body: raw}
		switch {
		case packAgentPromptRe.MatchString(rel):
			m := packAgentPromptRe.FindStringSubmatch(rel)
			cs.family, cs.packKey = "agents/prompts", m[1]
			includes["agents/"+m[2]] = cs
		case packPartialRe.MatchString(rel):
			m := packPartialRe.FindStringSubmatch(rel)
			cs.family, cs.packKey = "shared/partials", m[1]
			includes["partials/"+m[2]] = cs
		case packUnitRe.MatchString(rel):
			m := packUnitRe.FindStringSubmatch(rel)
			cs.family, cs.packKey = "shared/units", m[1]
			fmRaw, _ := splitMarkdownFrontmatter(raw)
			if fmRaw != "" {
				var ufm unitFrontmatter
				if err := yaml.Unmarshal([]byte(fmRaw), &ufm); err == nil {
					cs.attaches = ufm.Attaches
				}
			}
			cs.body = string(promptunit.Body([]byte(raw)))
		case packArchetypeRe.MatchString(rel):
			m := packArchetypeRe.FindStringSubmatch(rel)
			cs.family, cs.packKey = "shared/archetypes", m[1]
			includes["archetypes/"+m[2]] = cs
		case packGuidanceRe.MatchString(rel):
			m := packGuidanceRe.FindStringSubmatch(rel)
			cs.family, cs.packKey = "guidance", m[1]
		case packArchiveGuidRe.MatchString(rel):
			m := packArchiveGuidRe.FindStringSubmatch(rel)
			cs.family, cs.packKey = "archive/guidance", m[1]
		case packSkillBodyRe.MatchString(rel):
			m := packSkillBodyRe.FindStringSubmatch(rel)
			cs.family, cs.packKey, cs.skillName = "skills/SKILL.md", m[1], m[2]
			fmRaw, body := splitMarkdownFrontmatter(raw)
			cs.body = body
			if fmRaw != "" {
				var sfm skillFrontmatter
				if err := yaml.Unmarshal([]byte(fmRaw), &sfm); err == nil {
					if strings.TrimSpace(sfm.Name) != "" {
						cs.skillName = strings.TrimSpace(sfm.Name)
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
			}
			skillBodies[cs.packKey+":"+m[2]] = cs
		case packSkillRefRe.MatchString(rel):
			m := packSkillRefRe.FindStringSubmatch(rel)
			cs.family, cs.packKey, cs.skillName = "skills/references", m[1], m[2]
		default:
			r.Report("%s: unclassified markdown source outside reviewed prompt/skill families and explicit exclusions", srcLabel)
			continue
		}

		tree, err := pongoplain.ParseTemplateTree(cs.body)
		if err != nil {
			r.Report("%s: unhandled or invalid template expression/tag: %v", srcLabel, err)
			continue
		}
		cs.tree = tree
		parsed = append(parsed, cs)
	}

	for _, cs := range parsed {
		if cs.family == "skills/references" {
			parts := packSkillRefRe.FindStringSubmatch(strings.TrimPrefix(cs.rel, "config/"))
			if len(parts) == 3 {
				if parent, ok := skillBodies[parts[1]+":"+parts[2]]; ok {
					cs.hostResource = parent.hostResource
					cs.anyOfTools = parent.anyOfTools
					cs.skillName = parent.skillName
				}
			}
		}
	}
	return parsed, includes, skillBodies
}

// Every markdown source under lycaon/config is reconciled into a reviewed
// prompt/skill family or explicit non-template exclusion, parses through the
// closed Pongo expression-tree grammar, and resolves its render/include edges.
func stockSourcesReconciledAndParsed(r *invariants.Run) {
	parsed, includes, _ := classifyAndParseSources(r)
	if len(parsed) == 0 {
		return
	}
	byRel := make(map[string]*classifiedSource, len(parsed))
	for _, cs := range parsed {
		byRel[cs.rel] = cs
	}

	hasPartialsDir := len(includes) > 0
	var checkIncludes func(cs *classifiedSource, nodes []*pongoplain.TemplateNode)
	checkIncludes = func(cs *classifiedSource, nodes []*pongoplain.TemplateNode) {
		for _, n := range nodes {
			if n.Kind == pongoplain.NodeInclude && hasPartialsDir {
				if _, ok := includes[n.IncludeRef]; !ok {
					r.Report("%s:%d: {%% include %q %%} resolves to no discovered partial or archetype", cs.rel, n.Line, n.IncludeRef)
				}
			}
			for _, br := range n.Branches {
				checkIncludes(cs, br.Body)
			}
			checkIncludes(cs, n.Body)
			checkIncludes(cs, n.EmptyBody)
		}
	}
	for _, cs := range parsed {
		checkIncludes(cs, cs.tree.Nodes)
	}

	fullStockManifest := filepath.Join(r.Corpus.Root(), "lycaon", "config", "packs", "painted-wolf", "platform", "pack.yaml")
	if _, err := os.Stat(fullStockManifest); err != nil {
		return
	}
	reconcileFullStockEntryPoints(r, parsed, byRel)
}

func reconcileFullStockEntryPoints(r *invariants.Run, parsed []*classifiedSource, byRel map[string]*classifiedSource) {
	s := stock(r)
	for _, id := range s.Catalog.LoadedUnitIDs() {
		switch {
		case strings.HasPrefix(id, "agents/prompts/"),
			strings.HasPrefix(id, "shared/partials/"),
			strings.HasPrefix(id, "shared/units/"),
			strings.HasPrefix(id, "shared/archetypes/"),
			(strings.HasPrefix(id, "guidance/") && !strings.HasPrefix(id, "guidance/gate-feedback/")):
			unitPath, ok := s.Catalog.UnitPath(id)
			if !ok || !strings.HasSuffix(unitPath.String(), ".md") {
				continue
			}
			rel := "config/" + unitPath.String()
			if _, ok := byRel[rel]; !ok {
				r.Report("loaded catalog unit %s (%s) missing from reconciled source inventory", id, rel)
			}
		}
	}

	loadedSkills, err := StockSkills(s)
	r.Must(err)
	for _, sk := range loadedSkills {
		bodyRel := fmt.Sprintf("config/packs/%s/skills/%s/SKILL.md", sk.PackID, sk.Name)
		if _, ok := byRel[bodyRel]; !ok {
			r.Report("loaded stock skill %s (%s) missing from reconciled source inventory", sk.Name, bodyRel)
		}
		for _, res := range sk.Resources {
			if strings.HasSuffix(res, ".md") {
				refRel := path.Clean(fmt.Sprintf("config/packs/%s/skills/%s/%s", sk.PackID, sk.Name, res))
				if _, ok := byRel[refRel]; !ok {
					r.Report("loaded stock skill resource %s/%s (%s) missing from reconciled source inventory", sk.Name, res, refRel)
				}
				continue
			}
			raw, readErr := sk.ReadResource(res)
			if readErr == nil && (strings.Contains(string(raw), "{% if ") || strings.Contains(string(raw), "{% set ")) {
				r.Report("non-markdown skill resource %s/%s contains executable Pongo control tags", sk.Name, res)
			}
		}
	}

	guidanceIDs := map[string]bool{}
	promptNames := map[string]bool{}
	for _, cs := range parsed {
		if cs.family == "guidance" {
			if idx := strings.Index(cs.rel, "/guidance/"); idx >= 0 {
				guidanceIDs[strings.TrimSuffix(cs.rel[idx+len("/guidance/"):], ".md")] = true
			}
		} else if cs.family == "agents/prompts" {
			if idx := strings.Index(cs.rel, "/agents/prompts/"); idx >= 0 {
				promptNames["agents/"+cs.rel[idx+len("/agents/prompts/"):]] = true
			}
		}
	}
	if anchorReg, aErr := anchor.LoadRegistryFromConfigRoot(); aErr == nil {
		for _, b := range anchorReg.AllInform() {
			if b != nil && b.IsInform() && b.Render != "" && !guidanceIDs[b.Render] {
				r.Report("anchor inform render %q resolves to no reconciled guidance template", b.Render)
			}
		}
	}
	if wfReg, wErr := workflowdef.RegistryFromDirs(""); wErr == nil {
		for _, key := range sortedKeys(wfReg.All()) {
			for _, in := range wfReg.All()[key].Injects {
				if in.Render != "" && !guidanceIDs[in.Render] {
					r.Report("workflow %s inject render %q resolves to no reconciled guidance template", key, in.Render)
				}
			}
		}
	}
	if agents, agErr := agentdef.LoadEffectiveWithCatalog(s.Catalog); agErr == nil {
		for _, a := range agents {
			if a.SystemPromptTemplate != "" && !promptNames[a.SystemPromptTemplate] {
				r.Report("agent %s SystemPromptTemplate %q resolves to no reconciled prompt template", a.ID, a.SystemPromptTemplate)
			}
		}
	}
}

var workspaceIdentityOnlyFacts = map[string]bool{
	"has_git":                 true,
	"has_any_git_root":        true,
	"on_worker_branch":        true,
	"root_count":              true,
	"multi_root":              true,
	"has_workspace_roots":     true,
	"is_coordinator":          true,
	"session_mode":            true,
	"execution_mode":          true,
	"surface_id":              true,
	"tool_profile":            true,
	"session_scratch_dir":     true,
	"shared_temp_writable":    true,
	"shared_temp_locations":   true,
	"reviews_confined_writes": true,
	"command_external_hidden": true,
	"command_external_shown":  true,
}

var runnerDisjunctionFacts = map[string]bool{
	"profile_has_command":           true,
	"profile_command_external_exec": true,
	"profile_has_verify":            true,
	"profile_has_terminal_open":     true,
	"profile_has_terminal_send":     true,
}

var (
	gitCmdActionRe      = regexp.MustCompile("`git\\s+(log|status|diff|blame|show|rev-list|rev-parse|add|commit|bisect|restore|worktree)\\b")
	externalSelectorRe  = regexp.MustCompile("`external\\s+--")
	curlInCommandRe     = regexp.MustCompile("`curl(?:`|\\s+-)")
	prunedSchemaClaimRe = regexp.MustCompile("`(capability_request(?:\\.[a-z_]+)?|secret_use(?:\\.[a-z_]+)?|socks_proxy(?::\\s*true)?|snapshot_capture|terminal_capture|command\\([^`]*verification:\\s*true[^`]*\\))`")
	negatedClauseRe     = regexp.MustCompile(`(?i)(?:\bdo\s+not\b|\bnever\b|\bno\b|\bwithout\b|\bcannot\b|\brefused\b|\bnot\s+assign\b)[^.\n]*`)
)

type localAlias struct {
	name       string
	rawExpr    string
	isCapAlias bool
}

type activeGuard struct {
	rawExpr string
	expr    *pongoplain.Expr
	negated bool
}

func isBoolCapabilityFact(vocab prompts.ReaderFactVocabulary, ref string) bool {
	if workspaceIdentityOnlyFacts[ref] {
		return true
	}
	if strings.HasPrefix(ref, "can_") ||
		strings.HasPrefix(ref, "profile_has_") ||
		strings.HasPrefix(ref, "profile_can_request.") ||
		strings.HasPrefix(ref, "profile_command_") ||
		ref == "profile_mutation_capable" ||
		strings.HasPrefix(ref, "runner_") ||
		strings.HasPrefix(ref, "mints.") ||
		strings.HasPrefix(ref, "agent_has_skill_") ||
		strings.HasPrefix(ref, "visual_show_") ||
		strings.HasPrefix(ref, "card_may_") {
		return true
	}
	return vocab.AllowsReference(ref) && (strings.HasPrefix(ref, "has_") || strings.HasPrefix(ref, "is_"))
}

func comparesIdent(e *pongoplain.Expr, ident string) bool {
	if e == nil {
		return false
	}
	if e.Kind == pongoplain.ExprCompare {
		if (e.Left != nil && e.Left.Kind == pongoplain.ExprIdent && e.Left.Ident == ident) ||
			(e.Right != nil && e.Right.Kind == pongoplain.ExprIdent && e.Right.Ident == ident) {
			return true
		}
	}
	return comparesIdent(e.Cond, ident) || comparesIdent(e.Left, ident) || comparesIdent(e.Right, ident)
}

func isMultiRunnerDisjunction(e *pongoplain.Expr) bool {
	if e == nil {
		return false
	}
	hasOr := false
	runners := map[string]bool{}
	var walk func(*pongoplain.Expr)
	walk = func(n *pongoplain.Expr) {
		if n == nil {
			return
		}
		if n.Kind == pongoplain.ExprOr {
			hasOr = true
		}
		if n.Kind == pongoplain.ExprIdent && runnerDisjunctionFacts[n.Ident] {
			runners[n.Ident] = true
		}
		walk(n.Cond)
		walk(n.Left)
		walk(n.Right)
	}
	walk(e)
	// Count distinct tool families among command, verify, and terminal_*.
	families := map[string]bool{}
	for id := range runners {
		switch {
		case strings.Contains(id, "command"):
			families["command"] = true
		case strings.Contains(id, "verify"):
			families["verify"] = true
		case strings.Contains(id, "terminal"):
			families["terminal"] = true
		}
	}
	return hasOr && len(families) >= 2
}

// collectPositiveFacts extracts capability/tool facts established when g holds.
func collectPositiveFacts(e *pongoplain.Expr, negated bool, out map[string]bool) {
	if e == nil {
		return
	}
	switch e.Kind {
	case pongoplain.ExprNot:
		collectPositiveFacts(e.Left, !negated, out)
	case pongoplain.ExprAnd:
		if !negated {
			collectPositiveFacts(e.Left, false, out)
			collectPositiveFacts(e.Right, false, out)
		}
	case pongoplain.ExprOr:
		if negated {
			collectPositiveFacts(e.Left, true, out)
			collectPositiveFacts(e.Right, true, out)
		} else {
			leftSet := map[string]bool{}
			rightSet := map[string]bool{}
			collectPositiveFacts(e.Left, false, leftSet)
			collectPositiveFacts(e.Right, false, rightSet)
			// Intersect common capabilities, and also mark runner/verify union when all arms establish execution.
			for k := range leftSet {
				if rightSet[k] {
					out[k] = true
				}
			}
			if (leftSet["can_command_verify"] || leftSet["profile_has_verify"] || leftSet["runner_capability_request_tools"]) &&
				(rightSet["can_command_verify"] || rightSet["profile_has_verify"] || rightSet["runner_capability_request_tools"]) {
				out["any_exec_or_verify"] = true
			}
		}
	case pongoplain.ExprIdent:
		if !negated {
			out[e.Ident] = true
			markImpliedCapabilities(e.Ident, out)
		} else if e.Ident == "command_external_hidden" {
			out["command_external_shown"] = true
		}
	case pongoplain.ExprCompare:
		if e.Left != nil && e.Left.Kind == pongoplain.ExprIdent && e.Right != nil && e.Right.Kind == pongoplain.ExprLiteral {
			id := e.Left.Ident
			lit := e.Right.Literal
			truthy := (!negated && e.Op == "!=" && (lit == "false" || lit == "False" || lit == `""` || lit == "0")) ||
				(!negated && e.Op == "==" && (lit == "true" || lit == "True")) ||
				(negated && e.Op == "==" && (lit == "false" || lit == "False"))
			if truthy {
				out[id] = true
				markImpliedCapabilities(id, out)
			}
			falsyHidden := (!negated && e.Op == "==" && id == "command_external_hidden" && (lit == "false" || lit == "False"))
			if falsyHidden {
				out["command_external_shown"] = true
			}
		}
	}
}

func markImpliedCapabilities(fact string, out map[string]bool) {
	switch fact {
	case "can_git_read", "can_any_git_read":
		out["profile_has_command"] = true
		out["git_read_reach"] = true
	case "can_git_commit":
		out["profile_has_command"] = true
		out["profile_command_mutation"] = true
		out["git_read_reach"] = true
		out["git_commit_reach"] = true
	case "can_git_bisect":
		out["profile_has_command"] = true
		out["profile_command_mutation"] = true
		out["profile_command_external_exec"] = true
		out["git_read_reach"] = true
		out["git_commit_reach"] = true
	case "can_command_verify":
		out["profile_has_command"] = true
		out["profile_command_external_exec"] = true
	case "can_command_external_selector":
		out["profile_has_command"] = true
		out["profile_command_external_exec"] = true
		out["command_external_shown"] = true
	case "profile_command_mutation":
		out["profile_has_command"] = true
		out["git_commit_reach"] = true
	case "profile_command_external_exec", "profile_has_terminal_capture":
		out["profile_has_command"] = true
		out["profile_command_external_exec"] = true
	case "profile_command_network_egress":
		out["profile_has_command"] = true
	case "runner_capability_request_tools", "runner_host_resources_tools", "runner_direct_ip_tools",
		"runner_local_listen_tools", "runner_loopback_connect_tools", "runner_socket_tools", "runner_write_root_tools":
		out["runner_capability_request_tools"] = true
	}
}

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

func surfaceFloorSeed(rel string, surfacePlans map[string][]string) map[string]bool {
	out := map[string]bool{}
	const prefix = "coordinator-mode-"
	base := path.Base(rel)
	if !strings.HasPrefix(base, prefix) {
		return out
	}
	surfaceID := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(base, prefix), ".md"), "-", "_")
	for _, t := range surfacePlans[surfaceID] {
		out["profile_has_"+t] = true
	}
	return out
}

type capabilityChecker struct {
	run      *invariants.Run
	stock    *Stock
	vocab    prompts.ReaderFactVocabulary
	includes map[string]*classifiedSource
	reported map[string]bool
}

func (cc *capabilityChecker) emit(msg string) {
	if !cc.reported[msg] {
		cc.reported[msg] = true
		cc.run.Report("%s", msg)
	}
}

func isAuthoringMetaDoc(rel string) bool {
	return strings.Contains(rel, "/skills/write-a-skill/") ||
		strings.Contains(rel, "/skills/write-prompt-override/") ||
		strings.Contains(rel, "/skills/write-extension-pack/") ||
		strings.Contains(rel, "/skills/write-workflow/") ||
		strings.Contains(rel, "/skills/write-policy-rule/")
}

func buildSkillGuaranteedMap(s *Stock, skillBodies map[string]*classifiedSource) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for key, sb := range skillBodies {
		g := map[string]bool{}
		if sb.hostResource {
			g["runner_capability_request_tools"] = true
			g["runner_host_resources_tools"] = true
			g["profile_command_external_exec"] = true
		}
		if len(sb.anyOfTools) == 0 && sb.tree != nil {
			topText := directTextOf(sb.tree.Nodes)
			for _, t := range toolvocab.ToolsIn(s.Names, topText) {
				g["profile_has_"+t] = true
			}
			if strings.Contains(topText, "`tui#`") || strings.Contains(topText, "`terminal_capture`") {
				g["profile_command_external_exec"] = true
				g["runner_capability_request_tools"] = true
			}
			if strings.Contains(topText, "`socks_proxy") {
				g["runner_socks_proxy_tools"] = true
			}
			if strings.Contains(topText, "Native `curl` in `command`") {
				g["profile_command_network_egress"] = true
				g["runner_capability_request_tools"] = true
			}
		}
		out[key] = g
	}
	return out
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
	if authoringDoc {
		return
	}
	if externalSelectorRe.MatchString(segText) {
		hasSelector := established["can_command_external_selector"] ||
			(established["profile_command_external_exec"] && established["command_external_shown"])
		if !hasSelector {
			exprDesc := condExpr
			if exprDesc == "" {
				exprDesc = "<unguarded>"
			}
			cc.emit(fmt.Sprintf("%s:%d: expression %q promises action \"external --\" without selector visibility fact can_command_external_selector",
				sourceLabel, line, exprDesc))
		}
	}
	if ownCondExpr != nil && curlInCommandRe.MatchString(positiveText) && strings.Contains(positiveText, "`command`") {
		if slices.Contains(ownCondExpr.Identifiers(), "profile_has_command") && !established["profile_command_network_egress"] {
			cc.emit(fmt.Sprintf("%s:%d: expression %q promises action \"curl in command\" without profile_command_network_egress",
				sourceLabel, line, condExpr))
		}
	}
	if ownWorkspaceOnly {
		cc.checkWorkspaceOnlyActions(sourceLabel, line, condExpr, established, positiveText)
	}
}

func (cc *capabilityChecker) checkWorkspaceOnlyActions(sourceLabel string, line int, condExpr string, established map[string]bool, positiveText string) {
	for _, tool := range toolvocab.ToolsIn(cc.stock.Names, positiveText) {
		if established["profile_has_"+tool] ||
			(tool == "request_tools" && (established["more_tools_loadable"] || hasPrefixFact(established, "profile_can_request.") || hasPrefixFact(established, "can_request_"))) ||
			(tool == "command" && established["profile_has_command"]) {
			continue
		}
		cc.emit(fmt.Sprintf("%s:%d: expression %q promises tool %q using only workspace/identity facts (missing profile_has_%s or declared capability fact)",
			sourceLabel, line, condExpr, tool, tool))
	}
	for _, gm := range gitCmdActionRe.FindAllStringSubmatch(positiveText, -1) {
		sub := gm[1]
		switch sub {
		case "add", "commit":
			if !established["git_commit_reach"] {
				cc.emit(fmt.Sprintf("%s:%d: expression %q promises action \"git %s\" without declared capability fact can_git_commit",
					sourceLabel, line, condExpr, sub))
			}
		case "bisect", "worktree":
			if !established["profile_command_external_exec"] || !established["git_commit_reach"] {
				cc.emit(fmt.Sprintf("%s:%d: expression %q promises action \"git %s\" without declared capability fact can_git_bisect",
					sourceLabel, line, condExpr, sub))
			}
		default:
			if !established["git_read_reach"] && !established["profile_has_command"] {
				cc.emit(fmt.Sprintf("%s:%d: expression %q promises action \"git %s\" without declared capability fact can_git_read",
					sourceLabel, line, condExpr, sub))
			}
		}
	}
	for _, pm := range prunedSchemaClaimRe.FindAllStringSubmatch(positiveText, -1) {
		arg := pm[1]
		if !established["profile_command_external_exec"] && !established["runner_capability_request_tools"] && !established["runner_secret_use_tools"] && !established["runner_socks_proxy_tools"] {
			cc.emit(fmt.Sprintf("%s:%d: expression %q promises projected-schema argument %q without declared runner/external-exec capability fact",
				sourceLabel, line, condExpr, arg))
		}
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
	for _, g := range guards {
		collectPositiveFacts(g.expr, g.negated, established)
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

	for _, n := range nodes {
		switch n.Kind {
		case pongoplain.NodeText:
			if len(guards) > 0 && topCondRaw != "" {
				cc.inspectSegmentActions(sourceLabel, n.Line, topCondRaw, topCondExpr, ownWorkspaceOnly, established, aliases, n.Text, authoringDoc)
			} else if !authoringDoc && externalSelectorRe.MatchString(n.Text) {
				cc.inspectSegmentActions(sourceLabel, n.Line, "", nil, false, established, aliases, n.Text, false)
			}
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
	collectPositiveFacts(n.Expr.Cond, false, posEst)
	if n.Expr.Left != nil && n.Expr.Left.Kind == pongoplain.ExprLiteral {
		cc.inspectSegmentActions(sourceLabel, n.Line, n.RawExpr, n.Expr.Cond, wsOnly, posEst, aliases, n.Expr.Left.Literal, authoringDoc)
	}
	negEst := cloneBoolMap(established)
	collectPositiveFacts(n.Expr.Cond, true, negEst)
	if n.Expr.Right != nil && n.Expr.Right.Kind == pongoplain.ExprLiteral {
		cc.inspectSegmentActions(sourceLabel, n.Line, n.RawExpr, n.Expr.Cond, wsOnly, negEst, aliases, n.Expr.Right.Literal, authoringDoc)
	}
}

// Every shipped prompt, partial, unit, guidance template, skill body, and skill
// reference expresses action/tool/schema eligibility through declared host
// reader facts rather than local aliases, profile-ID checks, inline runner
// disjunctions, or workspace-metadata-only conditions.
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
	skillGuaranteed := buildSkillGuaranteedMap(s, skillBodies)
	cc := &capabilityChecker{
		run:      r,
		stock:    s,
		vocab:    vocab,
		includes: includes,
		reported: map[string]bool{},
	}

	for _, cs := range parsed {
		if cs.tree == nil {
			continue
		}
		seed := buildSourceSeed(cs, surfacePlans, skillGuaranteed)
		cc.checkNodes(cs, "", cs.tree.Nodes, seed, nil, map[string]localAlias{}, map[string]bool{cs.rel: true})
	}
}

func buildSourceSeed(cs *classifiedSource, surfacePlans map[string][]string, skillGuaranteed map[string]map[string]bool) map[string]bool {
	seed := surfaceFloorSeed(cs.rel, surfacePlans)
	for _, att := range cs.attaches {
		att = strings.TrimSpace(att)
		switch att {
		case "command_mutation":
			seed["profile_has_command"] = true
			seed["profile_command_mutation"] = true
			seed["git_commit_reach"] = true
		case "command_external_exec":
			seed["profile_has_command"] = true
			seed["profile_command_external_exec"] = true
		case "command_network_egress":
			seed["profile_has_command"] = true
			seed["profile_command_network_egress"] = true
		default:
			if att != "" {
				seed["profile_has_"+att] = true
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
	if cs.family == "shared/partials" || cs.family == "shared/archetypes" {
		seed["standalone_partial"] = true
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
