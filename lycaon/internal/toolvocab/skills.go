package toolvocab

import (
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
	"github.com/lycaon/lycaon/internal/sandbox"
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

// ReachCommandMutation marks a reader able to run command programs that mutate
// the workspace. Callers put it in a reachable map alongside tool names; copy
// naming a mutating command form stays reachable only while it is set.
const ReachCommandMutation = toolcontract.ReachCommandMutation

// profileHasCapabilityAliases lists the capability aliases behind
// profile_has_<alias> template facts that describe a surface capability rather
// than a single tool name.
var profileHasCapabilityAliases = map[string]bool{
	"write_tools":      true,
	"terminal_capture": true,
	"jq_edit":          true,
	"page_controls":    true,
	"managed_secrets":  true,
}

// profileHasIdentifier matches the profile_has_<name> template facts copy
// guards on, so validation can reject facts that name no tool.
var profileHasIdentifier = regexp.MustCompile(`profile_has_([a-z0-9_]+)`)

// UnreachableNames returns the tool, operation, and command-program names copy
// tells its reader to reach but the reachable map cannot, sorted. A name is
// reachable when the map holds it or when a reader with those facts can run it
// as a command program: command forms need `command`, mutating forms need
// ReachCommandMutation, and a dual-state form needs its tool. Callers render
// copy first so guarded branches resolve against the reader's actual facts.
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
	sort.Strings(out)
	return out
}

// profileReachable maps every registered tool to whether profileID holds it,
// adds tools the profile grants that this catalog does not register (a minimal
// catalog can lack host runners like `command`), and sets ReachCommandMutation
// when the profile runs mutating command programs.
func profileReachable(c *Catalog, profileID string) map[string]bool {
	held := make(map[string]bool, len(c.registered)+1)
	for name := range c.registered {
		held[name] = c.ProfileHolds(profileID, name)
	}
	if prof, ok := c.profiles[strings.TrimSpace(profileID)]; ok {
		for name := range prof.Tools {
			name = strings.TrimSpace(name)
			if name != "" && !c.registered[name] && plainToolName(name) {
				held[name] = prof.ToolAllowed(name)
			}
		}
	}
	held[ReachCommandMutation] = c.profileCommandMutates(profileID)
	return held
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

// profileCommandMutates reports whether profileID can run mutating command
// programs, matching the profile_command_mutation template fact.
func (c *Catalog) profileCommandMutates(profileID string) bool {
	prof, ok := c.profiles[strings.TrimSpace(profileID)]
	if !ok || !prof.ToolAllowed("command") {
		return false
	}
	mutation, err := toolcontract.NewCommandEffects(toolcontract.CommandMutation)
	if err != nil {
		return false
	}
	return prof.CommandEffects == 0 || prof.CommandEffects.Allows(mutation)
}

// skillRenderVars builds the template variables one skill text renders with for
// a reader: spawn policy and sizing facts, a profile_has_<tool> fact per held
// tool, derived surface capability aliases, and the caller shape. Unheld tools
// stay unset, which renders false. root_count is 1: validation checks the
// workspace case, where guarded command guidance is visible.
func skillRenderVars(held map[string]bool, coordinator bool) map[string]any {
	vars := spawn.PolicyTemplateVars(spawn.DefaultWorkerToolBudget())
	spawn.RefreshSizingHintVars(vars)
	for name, ok := range held {
		if ok && name != ReachCommandMutation {
			vars["profile_has_"+name] = true
		}
	}
	commandMutates := held["command"] && held[ReachCommandMutation]
	vars["profile_has_write_tools"] = held[writeToolMarker] || commandMutates
	vars["profile_command_mutation"] = commandMutates
	vars["profile_has_jq_edit"] = commandMutates
	vars["profile_has_terminal_capture"] = commandMutates || held["terminal_snapshot"] || held["terminal_read"]
	vars["profile_has_page_controls"] = held["page_open"] || held["page_act"] || held["page_snapshot"] || held["page_close"]
	vars["profile_has_managed_secrets"] = held["secret_generate"] && held["secret_list"] && held["secret_revoke"]
	vars["command_on_floor"] = held["command"]
	vars["is_coordinator"] = coordinator
	vars["has_git"] = true
	vars["root_count"] = 1
	return vars
}

// writeToolMarker mirrors prompts' write-tool surface: the one tool whose
// presence marks a profile as able to write (prompts.writeToolDisplayOrder).
const writeToolMarker = "restore_version"

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
		rendered, err := renderSkillText(text.body, vars)
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

// ValidateSkills checks every tool and command program a stock skill names is
// reachable by every profile the skill is offered to; instructions naming
// absent tools reject on contact. Each skill body and resource renders through
// its template with the selecting agent's facts first, so guarded branches
// resolve against what that reader actually holds. It also checks the template
// vocabulary itself: profile_has_ facts must name registered tools, and
// any_of_tools members must be registered tools. Skills the user supplied are
// the author's vocabulary and are left alone.
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
	for _, agent := range agents {
		held := profileReachable(c, agent.ProfileID)
		vars := skillRenderVars(held, agent.ProfileID == "coordinator")
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
			if !SkillToolsOffered(skill, held) {
				continue
			}
			problems = append(problems, skillReachProblems(c, agent, skill, held, vars)...)
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

// SkillToolsOffered reports whether offered covers every tool, retired
// operation, and command form the skill's rendered body unconditionally names,
// and satisfies sk's disjunctive requirement: when
// `metadata.paintedwolf.any_of_tools` names tools, at least one member must be
// offered or reachable as a command program. The body renders through
// pongoplain with the offered-tool template vars, so guarded branches for
// tools the reader lacks do not count against reachability. User-provided
// skills are not restricted.
func SkillToolsOffered(sk skills.Skill, offered map[string]bool) bool {
	if sk.UserProvided {
		return true
	}
	c := StockCatalog()
	if c == nil {
		return true
	}
	anyOf := sk.AnyOfTools()
	// any_of members are individually optional in the body scan; the
	// disjunction below decides usability.
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

// renderSkillBody renders sk.Body with the offered-tool template vars so
// profile-guarded branches resolve against the reader's actual tool set.
// Compilation or execution failures fall back to the raw body.
func renderSkillBody(sk skills.Skill, offered map[string]bool) string {
	rendered, err := renderSkillText(sk.Body, skillRenderVars(offered, offered["task"]))
	if err != nil {
		return sk.Body
	}
	return rendered
}
