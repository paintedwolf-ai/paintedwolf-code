package toolvocab

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/spawn"
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

// ValidateSkills checks every tool a stock skill names is held by every profile
// the skill is offered to; instructions naming absent tools reject on contact.
// Skills the user supplied are the author's vocabulary and are left alone.
func ValidateSkills(c *Catalog, agents []AgentSurface, loaded []skills.Skill) error {
	byName := make(map[string]skills.Skill, len(loaded))
	bodies := make(map[string]string, len(loaded))
	var problems []string
	for _, s := range loaded {
		byName[s.Name] = s
		bodies[s.Name] = s.Body
		if s.UserProvided {
			continue
		}
	}
	for _, agent := range agents {
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
			for _, tool := range NamedTools(c, bodies[name]) {
				if c.ProfileHolds(agent.ProfileID, tool) {
					continue
				}
				problems = append(problems, fmt.Sprintf(
					"skill %q tells its reader to call %q, but agent %q runs on profile %q, which does not hold it — "+
						"grant the tool, drop the skill from that agent, or guard the mention with {%% if profile_has_%s %%}",
					skill.Name, tool, agent.AgentID, agent.ProfileID, tool))
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

var stockCatalog = sync.OnceValue(func() *Catalog {
	c, _ := NewCatalog(&toolschema.Config{}, []sandbox.ToolProfile{{ID: "stock"}}, Provenance{})
	return c
})

// SkillToolsOffered reports whether offered covers every tool the skill's
// rendered body names and satisfies sk's disjunctive requirement: when
// `metadata.paintedwolf.any_of_tools` names tools, at least one member must be
// offered. The body renders through pongoplain with the offered-tool template
// vars, so guarded branches for tools the reader lacks do not count against
// reachability. User-provided skills are not restricted.
func SkillToolsOffered(sk skills.Skill, offered map[string]bool) bool {
	if sk.UserProvided {
		return true
	}
	c := stockCatalog()
	if c == nil {
		return true
	}
	anyOf := sk.AnyOfTools()
	for _, tool := range NamedTools(c, renderSkillBody(sk, offered)) {
		if offered[tool] {
			continue
		}
		// any_of members are individually optional; the disjunction below
		// decides usability.
		if !slices.Contains(anyOf, tool) {
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
	vars := spawn.PolicyTemplateVars(spawn.DefaultWorkerToolBudget())
	for tool, ok := range offered {
		if ok {
			vars["profile_has_"+tool] = true
		}
	}
	vars["is_coordinator"] = offered["task"]
	vars["has_git"] = true
	tpl, err := pongoplain.Compile(sk.Body)
	if err != nil {
		return sk.Body
	}
	rendered, err := pongoplain.ExecuteDetached(tpl, vars)
	if err != nil {
		return sk.Body
	}
	return rendered
}
