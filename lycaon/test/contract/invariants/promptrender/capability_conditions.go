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
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
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

		cs, ok := classifySingleMarkdown(r, rel, srcLabel, raw, includes, skillBodies)
		if !ok {
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

func classifySingleMarkdown(
	r *invariants.Run,
	rel, srcLabel, raw string,
	includes map[string]*classifiedSource,
	skillBodies map[string]*classifiedSource,
) (*classifiedSource, bool) {
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
		return nil, false
	}
	return cs, true
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

var workspaceIdentityOnlyFacts = buildWorkspaceIdentityOnlyFacts()

func buildWorkspaceIdentityOnlyFacts() map[string]bool {
	out := map[string]bool{}
	for name, spec := range prompts.DeclaredReaderFactScalars() {
		if strings.HasPrefix(name, "can_") ||
			strings.HasPrefix(name, "profile_") ||
			strings.HasPrefix(name, "runner_") ||
			strings.HasPrefix(name, "tool_check_") ||
			strings.HasPrefix(name, "visual_show_") ||
			strings.HasPrefix(name, "card_may_") ||
			strings.HasPrefix(name, "caps.") {
			continue
		}
		if spec.Type == "[]string" && name != "shared_temp_locations" {
			continue
		}
		if name == "has_file_tools" || name == "command_on_floor" || name == "more_tools_loadable" ||
			name == "recall_may_widen" || name == "web_search_enabled" || name == "agent_tools" {
			continue
		}
		out[name] = true
	}
	return out
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
	prunedSchemaClaimRe = buildPrunedSchemaClaimRegex()
	negatedClauseRe     = regexp.MustCompile(`(?i)(?:\bdo\s+not\b|\bnever\b|\bno\b|\bwithout\b|\bcannot\b|\brefused\b|\bnot\s+assign\b|\bunavailable\b)[^.\n]*`)
)

func buildPrunedSchemaClaimRegex() *regexp.Regexp {
	var alts []string
	if idx, err := toolvocabtest.StockPrunableSchemaIndex(); err == nil && idx != nil {
		hasDotByTop := map[string]bool{}
		var topOrder []string
		for _, tool := range idx.AllPrunableTools() {
			if tool == "ask_user" || tool == "wait" {
				continue
			}
			for _, p := range idx.PrunablePaths(tool) {
				top, _, hasDot := strings.Cut(p, ".")
				if _, seen := hasDotByTop[top]; !seen {
					hasDotByTop[top] = false
					topOrder = append(topOrder, top)
				}
				if hasDot {
					hasDotByTop[top] = true
				}
			}
		}
		slices.Sort(topOrder)
		for _, top := range topOrder {
			if top == "verification" {
				alts = append(alts, `command\([^`+"`"+`]*verification:\s*true[^`+"`"+`]*\)`)
				continue
			}
			escaped := regexp.QuoteMeta(top)
			if hasDotByTop[top] {
				alts = append(alts, escaped+`(?:\.[a-z_]+)?`)
			} else {
				alts = append(alts, escaped+`(?::\s*true)?`)
			}
		}
		for _, ek := range idx.AllPrunableEnums() {
			if ek.Tool == "ask_user" && ek.Path == "response_type" {
				alts = append(alts, regexp.QuoteMeta(ek.Path)+`:\s*`+regexp.QuoteMeta(ek.Literal))
			}
		}
	}
	if len(alts) == 0 {
		return regexp.MustCompile(`$^`)
	}
	return regexp.MustCompile("`(" + strings.Join(alts, "|") + ")`")
}

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
	if prompts.IsBooleanReaderFact(ref) {
		return true
	}
	for _, fam := range prompts.ReaderFactFamilies() {
		if fam.Type != "bool" {
			continue
		}
		if fam.Nested && (ref == fam.Prefix || strings.HasPrefix(ref, fam.Prefix+".")) {
			return vocab.AllowsReference(ref) || ref == fam.Prefix
		}
		if !fam.Nested && strings.HasPrefix(ref, fam.Prefix) {
			return true
		}
	}
	return false
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

func collectPositiveFacts(e *pongoplain.Expr, negated bool, negatedFacts map[string]bool, schemas *toolschema.Config, out map[string]bool) {
	if e == nil {
		return
	}
	switch e.Kind {
	case pongoplain.ExprNot:
		collectPositiveFacts(e.Left, !negated, negatedFacts, schemas, out)
	case pongoplain.ExprAnd:
		if !negated {
			collectPositiveFacts(e.Left, false, negatedFacts, schemas, out)
			collectPositiveFacts(e.Right, false, negatedFacts, schemas, out)
		}
	case pongoplain.ExprOr:
		if negated {
			collectPositiveFacts(e.Left, true, negatedFacts, schemas, out)
			collectPositiveFacts(e.Right, true, negatedFacts, schemas, out)
		} else {
			if exprFalsifiedBy(e.Left, negatedFacts) {
				collectPositiveFacts(e.Right, false, negatedFacts, schemas, out)
				return
			}
			if exprFalsifiedBy(e.Right, negatedFacts) {
				collectPositiveFacts(e.Left, false, negatedFacts, schemas, out)
				return
			}
			leftSet := map[string]bool{}
			rightSet := map[string]bool{}
			collectPositiveFacts(e.Left, false, negatedFacts, schemas, leftSet)
			collectPositiveFacts(e.Right, false, negatedFacts, schemas, rightSet)
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
			markImpliedCapabilities(e.Ident, schemas, out)
		} else {
			negatedFacts[e.Ident] = true
			if e.Ident == "command_external_hidden" {
				out["command_external_shown"] = true
			}
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
				markImpliedCapabilities(id, schemas, out)
			}
			falsyHidden := (!negated && e.Op == "==" && id == "command_external_hidden" && (lit == "false" || lit == "False"))
			if falsyHidden {
				out["command_external_shown"] = true
			}
		}
	}
}

func exprFalsifiedBy(e *pongoplain.Expr, negatedFacts map[string]bool) bool {
	if e == nil || len(negatedFacts) == 0 {
		return false
	}
	if e.Kind == pongoplain.ExprIdent {
		return negatedFacts[e.Ident]
	}
	if e.Kind == pongoplain.ExprAnd {
		return exprFalsifiedBy(e.Left, negatedFacts) || exprFalsifiedBy(e.Right, negatedFacts)
	}
	return false
}

func minimumProductionProjectedSchema(toolName string, commandReadOnly bool, schemas *toolschema.Config) (map[string]any, bool) {
	if schemas == nil {
		return nil, false
	}
	meta, ok := schemas.ToolMeta(toolName)
	if !ok {
		return nil, false
	}
	proj := toolschema.ProjectToolArgsSchema(toolName, meta.ArgsSchema, toolschema.ProjectOptions{
		CommandReadOnly:     toolName == "command" && commandReadOnly,
		PruneWaitConditions: toolName == "wait",
		WaitConditions:      nil,
		AskMode:             toolName == "ask_user",
	})
	return proj, true
}

func markProjectedSchemaFieldsForTool(toolName string, commandReadOnly bool, schemas *toolschema.Config, out map[string]bool) {
	proj, ok := minimumProductionProjectedSchema(toolName, commandReadOnly, schemas)
	if !ok {
		return
	}
	for _, field := range toolschema.ArgFieldPaths(proj) {
		out["schema_accepts:"+field] = true
	}
}

func markImpliedCapabilities(fact string, schemas *toolschema.Config, out map[string]bool) {
	if strings.HasPrefix(fact, "profile_has_") {
		toolName := strings.TrimPrefix(fact, "profile_has_")
		if toolName == "command" {
			markProjectedSchemaFieldsForTool("command", !out["profile_command_external_exec"], schemas, out)
		} else if toolName == "terminal_capture" {
			out["profile_has_command"] = true
			out["profile_command_external_exec"] = true
			markProjectedSchemaFieldsForTool("command", false, schemas, out)
		} else {
			markProjectedSchemaFieldsForTool(toolName, false, schemas, out)
		}
	}
	switch fact {
	case "can_ask_user_secret":
		out["profile_has_ask_user"] = true
		out["schema_accepts:secret"] = true
		out["schema_accepts:response_type"] = true
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
		out["git_bisect_reach"] = true
		markProjectedSchemaFieldsForTool("command", false, schemas, out)
	case "can_command_verify", "profile_command_external_exec":
		out["profile_has_command"] = true
		out["profile_command_external_exec"] = true
		out["profile_has_terminal_capture"] = true
		markProjectedSchemaFieldsForTool("command", false, schemas, out)
	case "can_command_external_selector":
		out["profile_has_command"] = true
		out["profile_command_external_exec"] = true
		out["profile_has_terminal_capture"] = true
		out["command_external_shown"] = true
		markProjectedSchemaFieldsForTool("command", false, schemas, out)
	case "profile_command_mutation", "profile_command_network_egress":
		out["profile_has_command"] = true
	case "runner_capability_request_tools":
		out["schema_accepts:capability_request"] = true
	case "runner_host_resources_tools":
		out["runner_capability_request_tools"] = true
		out["schema_accepts:capability_request"] = true
		out["schema_accepts:capability_request.host_resources"] = true
	case "runner_direct_ip_tools":
		out["runner_capability_request_tools"] = true
		out["schema_accepts:capability_request"] = true
		out["schema_accepts:capability_request.direct_ip"] = true
	case "runner_local_listen_tools":
		out["runner_capability_request_tools"] = true
		out["schema_accepts:capability_request"] = true
		out["schema_accepts:capability_request.local_listen"] = true
	case "runner_loopback_connect_tools":
		out["runner_capability_request_tools"] = true
		out["schema_accepts:capability_request"] = true
		out["schema_accepts:capability_request.loopback_connect"] = true
	case "runner_socket_tools":
		out["runner_capability_request_tools"] = true
		out["schema_accepts:capability_request"] = true
		out["schema_accepts:capability_request.socket_paths"] = true
	case "runner_write_root_tools":
		out["runner_capability_request_tools"] = true
		out["schema_accepts:capability_request"] = true
		out["schema_accepts:capability_request.write_root"] = true
	case "runner_secret_use_tools":
		out["schema_accepts:secret_use"] = true
		out["schema_accepts:secret_use.services"] = true
	case "runner_socks_proxy_tools":
		out["schema_accepts:socks_proxy"] = true
	}
}
