package toolvocab

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
)

// AgentSurface pairs an agent with the profile its tools come from.
type AgentSurface struct {
	AgentID   string
	ProfileID string
	Skills    skills.Selector
}

// AgentSurfaces projects agents onto their profiles and derives which profile
// plays which role. An agent naming a profile this catalog does not carry is
// skipped: ValidateAgentToolProfiles performs that check, and answering it twice
// would report it against whichever caller ran first.
func AgentSurfaces(agents []agentdef.Profile, profiles []sandbox.ToolProfile) ([]AgentSurface, Surfaces, error) {
	if len(agents) == 0 {
		return nil, Surfaces{}, fmt.Errorf("no agent profiles")
	}
	known := make(map[string]bool, len(profiles))
	for _, p := range profiles {
		known[p.ID] = true
	}
	var out []AgentSurface
	var surfaces Surfaces
	workers := map[string]bool{}
	for _, agent := range agents {
		pid := strings.TrimSpace(agent.ToolProfile)
		if !known[pid] {
			continue
		}
		out = append(out, AgentSurface{AgentID: agent.ID, ProfileID: pid, Skills: agent.Skills})
		for _, role := range agent.TopologyRoles {
			switch strings.TrimSpace(role) {
			case agentdef.TopologyRoleCoordinator:
				if surfaces.Coordinator != "" && surfaces.Coordinator != pid {
					return nil, Surfaces{}, fmt.Errorf(
						"two coordinator profiles (%q and %q) — the coordinator audience would be ambiguous",
						surfaces.Coordinator, pid)
				}
				surfaces.Coordinator = pid
			case agentdef.TopologyRoleWorker:
				workers[pid] = true
			}
		}
	}
	if surfaces.Coordinator == "" {
		return nil, Surfaces{}, fmt.Errorf("no agent declares the %s topology role", agentdef.TopologyRoleCoordinator)
	}
	for pid := range workers {
		surfaces.Workers = append(surfaces.Workers, pid)
	}
	sort.Strings(surfaces.Workers)
	sort.Slice(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return out, surfaces, nil
}

// ReachCommandMutation marks a reader able to run mutating command programs.
const ReachCommandMutation = toolcontract.ReachCommandMutation

var profileHasCapabilityAliases = map[string]bool{
	"write_tools":      true,
	"terminal_capture": true,
	"jq_edit":          true,
	"page_controls":    true,
	"managed_secrets":  true,
}

var profileHasIdentifier = regexp.MustCompile(`profile_has_([a-z0-9_]+)`)

// UnreachableNames returns the tool, operation, command-program, and evidence
// handle names (as "kind#") that corpus names but reachable cannot supply.
func UnreachableNames(c *Catalog, corpus string, reachable map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, name := range ToolsIn(c, corpus) {
		if reachable[name] || toolcontract.CommandLanguageReachable(name, reachable) {
			continue
		}
		add(name)
	}
	for _, name := range toolcontract.HyphenatedCommandFormsIn(corpus) {
		if c.registered[name] || c.operations[name] {
			continue
		}
		if reachable[name] || toolcontract.CommandLanguageReachable(name, reachable) {
			continue
		}
		add(name)
	}
	for _, handle := range c.unmintableHandles(corpus, reachable) {
		add(handle)
	}
	sort.Strings(out)
	return out
}

// plainToolName reports whether name can appear as a template fact suffix.
func plainToolName(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_':
		default:
			return false
		}
	}
	return name != "" && name[0] >= 'a' && name[0] <= 'z'
}

func (c *Catalog) profileReader(profileID string) (prompts.Reader, error) {
	prof, ok := c.profiles[strings.TrimSpace(profileID)]
	if !ok {
		return prompts.Reader{}, fmt.Errorf("unknown tool profile %q", profileID)
	}
	var visible []string
	loaded := map[string]bool{}
	for name := range c.registered {
		if c.ProfileHolds(profileID, name) {
			visible = append(visible, name)
			loaded[name] = true
		}
	}
	for name := range prof.Tools {
		name = strings.TrimSpace(name)
		if name != "" && !loaded[name] && plainToolName(name) && prof.ToolAllowed(name) {
			visible = append(visible, name)
			loaded[name] = true
		}
	}
	sort.Strings(visible)
	if prof.CommandEffects == 0 && prof.ToolAllowed("command") {
		prof.CommandEffects = toolcontract.AllCommandEffects()
	}
	return prompts.NewReader(prompts.ReaderInput{
		ProfileID: prof.ID,
		Profiles:  []sandbox.ToolProfile{prof},
		Visible:   visible,
		Loaded:    loaded,
		Schemas:   c.schemas,
		RootCount: 1,
		HasGit:    true,
		WebSearch: true,
	})
}

func offeredReader(offered map[string]bool) (prompts.Reader, error) {
	profileID := "worker"
	if offered["task"] {
		profileID = prompts.CoordinatorProfileID
	}
	tools := make(map[string]bool, len(offered))
	var visible []string
	for name, ok := range offered {
		if !ok || toolcontract.IsReachMark(name) || !plainToolName(name) {
			continue
		}
		tools[name] = true
		visible = append(visible, name)
	}
	sort.Strings(visible)
	var effects toolcontract.CommandEffects
	if tools["command"] {
		classes := []toolcontract.CommandEffectClass{toolcontract.CommandReadOnly}
		if offered[toolcontract.ReachCommandMutation] {
			classes = append(classes, toolcontract.CommandMutation)
		}
		if offered[toolcontract.ReachCommandExternalExec] {
			classes = append(classes, toolcontract.CommandExternalExec)
		}
		if offered[toolcontract.ReachCommandNetworkEgress] {
			classes = append(classes, toolcontract.CommandNetworkEgress)
		}
		effects, _ = toolcontract.NewCommandEffects(classes...)
	}
	return prompts.NewReader(prompts.ReaderInput{
		ProfileID: profileID,
		Profiles: []sandbox.ToolProfile{{
			ID:             profileID,
			Tools:          tools,
			CommandEffects: effects,
		}},
		Visible:   visible,
		Loaded:    tools,
		RootCount: 1,
		HasGit:    true,
		WebSearch: true,
	})
}

type skillRenderMemo struct {
	out string
	err error
}

var skillRenderCache = scopedstore.New[skillRenderMemo](2048)

func varsDigestKey(vars map[string]any) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%v", vars)
	return string(h.Sum(nil))
}

func renderSkillTextCached(varsKey, text string, vars map[string]any) (string, error) {
	key := varsKey + "\x00" + text
	if hit, ok := skillRenderCache.Load(key); ok {
		return hit.out, hit.err
	}
	out, err := renderSkillText(text, vars)
	skillRenderCache.Store(key, skillRenderMemo{out: out, err: err})
	return out, err
}

// renderSkillText renders one skill text through its template.
func renderSkillText(text string, vars map[string]any) (string, error) {
	tpl, err := pongoplain.Compile(text)
	if err != nil {
		return "", err
	}
	return pongoplain.ExecuteDetached(tpl, vars)
}

// guardSuggestion names the profile_has_ fact that gates name for readers: the
// tool itself, or the dual-state tool behind a command form. Empty when no
// single fact gates it.
func guardSuggestion(c *Catalog, name string) string {
	if c.KnownName(name) {
		return name
	}
	return toolcontract.CommandFormToolOf(name)
}

// skillVocabularyProblems checks the template vocabulary a skill authors
// against the catalog (INV-SK-07): every profile_has_<name> fact must name a
// registered tool or the write_tools alias, and every any_of_tools member must
// be a registered tool.
func skillVocabularyProblems(c *Catalog, skill skills.Skill) []string {
	var problems []string
	texts := []string{skill.Body}
	for _, rel := range skill.MarkdownResources() {
		body, err := skill.ReadResource(rel)
		if err != nil {
			problems = append(problems, fmt.Sprintf(
				"skill %q: resource %q is unreadable, so its reachability cannot be checked: %v",
				skill.Name, rel, err))
			continue
		}
		texts = append(texts, string(body))
	}
	for _, text := range texts {
		for _, m := range profileHasIdentifier.FindAllStringSubmatch(text, -1) {
			name := m[1]
			if profileHasCapabilityAliases[name] || c.KnownName(name) {
				continue
			}
			problems = append(problems, fmt.Sprintf(
				"skill %q guards on profile_has_%s, which names no tool this host registers — "+
					"guard on a registered tool so the branch can resolve for some reader",
				skill.Name, name))
		}
	}
	for _, tool := range skill.AnyOfTools() {
		if c.KnownName(tool) {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"skill %q lists %q in any_of_tools, which resolves to no registered tool — "+
				"the disjunction can never be satisfied",
			skill.Name, tool))
	}
	return problems
}

// skillReachProblems renders the skill's body and resources for one selecting
// agent and reports every name the rendered copy reaches for that the agent's
// profile cannot supply.
func skillReachProblems(
	c *Catalog, agent AgentSurface, skill skills.Skill,
	held map[string]bool, vars map[string]any,
	render func(body string, vars map[string]any) (string, error),
) []string {
	var problems []string
	texts := []struct{ rel, body string }{{"", skill.Body}}
	for _, rel := range skill.MarkdownResources() {
		body, err := skill.ReadResource(rel)
		if err != nil {
			problems = append(problems, fmt.Sprintf(
				"skill %q: resource %q is unreadable, so its reachability cannot be checked: %v",
				skill.Name, rel, err))
			continue
		}
		texts = append(texts, struct{ rel, body string }{rel, string(body)})
	}
	for _, text := range texts {
		rendered, err := render(text.body, vars)
		if err != nil {
			problems = append(problems, fmt.Sprintf(
				"skill %q: %s does not render for agent %q, so reading it fails at activation: %v",
				skill.Name, relLabel(text.rel), agent.AgentID, err))
			continue
		}
		for _, name := range UnreachableNames(c, rendered, held) {
			if guard := guardSuggestion(c, name); guard != "" {
				problems = append(problems, fmt.Sprintf(
					"skill %q: %s tells its reader to call %q, but agent %q runs on profile %q, which does not hold it — "+
						"grant the tool, drop the skill from that agent, or guard the mention with {%% if profile_has_%s %%}",
					skill.Name, relLabel(text.rel), name, agent.AgentID, agent.ProfileID, guard))
				continue
			}
			problems = append(problems, fmt.Sprintf(
				"skill %q: %s tells its reader to run %q, but agent %q runs on profile %q, which cannot reach that program — "+
					"drop the skill from that agent, guard the mention on a fact the reader satisfies, or name a program every reader can reach",
				skill.Name, relLabel(text.rel), name, agent.AgentID, agent.ProfileID))
		}
	}
	return problems
}

func relLabel(rel string) string {
	if rel == "" {
		return "body"
	}
	return "resource " + rel
}

// skillRenderVariant is one alternative environment state validation renders.
type skillRenderVariant struct {
	label string
	vars  map[string]any
}

func skillRenderVarVariants(vars map[string]any) []skillRenderVariant {
	fork := func(key string, value any) map[string]any {
		variant := make(map[string]any, len(vars)+1)
		for k, v := range vars {
			variant[k] = v
		}
		variant[key] = value
		return variant
	}
	return []skillRenderVariant{
		{label: "has_git=false", vars: fork("has_git", false)},
		{label: "root_count=0", vars: fork("root_count", 0)},
		{label: "root_count=2", vars: fork("root_count", 2)},
	}
}

// ValidateSkills checks every tool and command program a stock skill names is
// reachable by every profile the skill is offered to.
func ValidateSkills(c *Catalog, agents []AgentSurface, loaded []skills.Skill) error {
	byName := make(map[string]skills.Skill, len(loaded))
	var problems []string
	for _, s := range loaded {
		byName[s.Name] = s
		if s.UserProvided {
			continue
		}
		problems = append(problems, skillVocabularyProblems(c, s)...)
	}
	offeredByProfile := map[string]bool{}
	for _, agent := range agents {
		reader, err := c.profileReader(agent.ProfileID)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		held := reader.Held()
		vars := prompts.SkillVars(reader, spawn.DefaultWorkerToolBudget())
		defaultKey := varsDigestKey(vars)
		variants := skillRenderVarVariants(vars)
		variantKeys := make([]string, len(variants))
		for i, v := range variants {
			variantKeys[i] = varsDigestKey(v.vars)
		}
		memoRender := func(varsKey string) func(string, map[string]any) (string, error) {
			return func(body string, renderVars map[string]any) (string, error) {
				return renderSkillTextCached(varsKey, body, renderVars)
			}
		}
		seen := map[string]bool{}
		report := func(problem, label string) {
			if seen[problem] {
				return
			}
			seen[problem] = true
			if label != "" {
				problem += " [rendered with " + label + "]"
			}
			problems = append(problems, problem)
		}
		for _, name := range offeredSkills(agent.Skills, loaded) {
			skill, ok := byName[name]
			if !ok {
				problems = append(problems, fmt.Sprintf(
					"agent %q selects skill %q, which is not loaded", agent.AgentID, name))
				continue
			}
			if skill.UserProvided {
				continue
			}
			// Runtime and the contract matrix only offer a skill to readers
			// whose tools cover its rendered body (SkillToolsOffered), so
			// validation checks the same eligibility set rather than demanding
			// every skill render clean for every profile.
			offerKey := agent.ProfileID + "\x00" + skill.Name + "\x00" + skill.Body
			isOffered, cachedOffer := offeredByProfile[offerKey]
			if !cachedOffer {
				isOffered = SkillToolsOffered(skill, held)
				offeredByProfile[offerKey] = isOffered
			}
			if !isOffered {
				continue
			}
			for _, problem := range skillReachProblems(c, agent, skill, held, vars, memoRender(defaultKey)) {
				report(problem, "")
			}
			for i, variant := range variants {
				for _, problem := range skillReachProblems(c, agent, skill, held, variant.vars, memoRender(variantKeys[i])) {
					report(problem, variant.label)
				}
			}
		}
	}
	return joinProblems(problems)
}

func offeredSkills(sel skills.Selector, loaded []skills.Skill) []string {
	if sel.Empty() {
		return nil
	}
	if sel.All {
		out := make([]string, 0, len(loaded))
		for _, s := range loaded {
			out = append(out, s.Name)
		}
		sort.Strings(out)
		return out
	}
	out := append([]string(nil), sel.Names...)
	sort.Strings(out)
	return out
}

// StockCatalog returns the bundled stock tool vocabulary (native manifest plus
// pack tool and operation schemas).
func StockCatalog() *Catalog {
	if c := bundledStockCatalog(); c != nil {
		return c
	}
	return fallbackStockCatalog()
}

// bundledStockCatalog builds the full stock vocabulary from the bundled
// configuration tree, so the gate recognizes tool names without a device
// catalog: NewCatalog merges the native manifest with the pack's tool and
// operation schemas.
var bundledStockCatalog = sync.OnceValue(func() *Catalog {
	schemas, err := loadBundledToolSchemas()
	if err != nil {
		return nil
	}
	c, err := NewCatalog(schemas, []sandbox.ToolProfile{{ID: "stock"}}, Provenance{})
	if err != nil {
		return nil
	}
	return c
})

// loadBundledToolSchemas reads the stock pack's tool and operation schema
// directories from the bundled configuration tree.
func loadBundledToolSchemas() (*toolschema.Config, error) {
	callable, err := readBundledSchemaDir(config.ToolSchemasDir)
	if err != nil {
		return nil, err
	}
	operations, err := readBundledSchemaDir(config.PlatformTools.Join("operation-schemas"))
	if err != nil {
		return nil, err
	}
	return toolschema.Merge(callable, operations)
}

// readBundledSchemaDir walks a bundled schema directory for *.yaml
// Entry-at-root files, mirroring toolschema.LoadSchemaDir on an fs.FS.
func readBundledSchemaDir(dir config.Rel) (*toolschema.Config, error) {
	fsys := config.Source()
	ents, err := fs.ReadDir(fsys, dir.String())
	if err != nil {
		return nil, err
	}
	out := make(map[string]toolschema.Entry)
	for _, ent := range ents {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		stem := strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml")
		if !toolschema.ValidToolName(stem) {
			return nil, fmt.Errorf("invalid tool schema stem %q in %s", stem, dir)
		}
		data, err := fs.ReadFile(fsys, dir.Join(name).String())
		if err != nil {
			return nil, err
		}
		entry, err := toolschema.ParseEntry(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if _, dup := out[stem]; dup {
			return nil, fmt.Errorf("duplicate tool schema stem %q in %s", stem, dir)
		}
		out[stem] = entry
	}
	return toolschema.ConfigFromEntries(out), nil
}

// fallbackStockCatalog is the host-produced vocabulary for processes with no
// active pack set: host tools and command-language operations only.
var fallbackStockCatalog = sync.OnceValue(func() *Catalog {
	c, _ := NewCatalog(&toolschema.Config{}, []sandbox.ToolProfile{{ID: "stock"}}, Provenance{})
	return c
})

// SkillToolsOffered reports whether offered covers every tool, operation, and
// command form sk's rendered body requires, and satisfies any_of_tools.
func SkillToolsOffered(sk skills.Skill, offered map[string]bool) bool {
	if sk.UserProvided {
		return true
	}
	c := StockCatalog()
	if c == nil {
		return true
	}
	anyOf := sk.AnyOfTools()
	for _, name := range UnreachableNames(c, renderSkillBody(sk, offered), offered) {
		if !slices.Contains(anyOf, name) {
			return false
		}
	}
	if len(anyOf) == 0 {
		return true
	}
	for _, tool := range anyOf {
		if offered[tool] {
			return true
		}
	}
	return false
}

var offeredSkillVarsCache = scopedstore.New[map[string]any](64)

func offeredSkillVars(offered map[string]bool) (string, map[string]any, error) {
	keys := make([]string, 0, len(offered))
	for name, ok := range offered {
		if ok {
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)
	cacheKey := "offered:" + strings.Join(keys, "\x00")
	if vars, ok := offeredSkillVarsCache.Load(cacheKey); ok {
		return cacheKey, vars, nil
	}
	reader, err := offeredReader(offered)
	if err != nil {
		return "", nil, err
	}
	vars := prompts.SkillVars(reader, spawn.DefaultWorkerToolBudget())
	offeredSkillVarsCache.Store(cacheKey, vars)
	return cacheKey, vars, nil
}

func renderSkillBody(sk skills.Skill, offered map[string]bool) string {
	cacheKey, vars, err := offeredSkillVars(offered)
	if err != nil {
		return sk.Body
	}
	rendered, err := renderSkillTextCached(cacheKey, sk.Body, vars)
	if err != nil {
		return sk.Body
	}
	return rendered
}
