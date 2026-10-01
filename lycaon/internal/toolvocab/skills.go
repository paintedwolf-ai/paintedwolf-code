package toolvocab

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
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
// optional_tools covers names a skill mentions without depending on. Skills the
// user supplied are the author's vocabulary and are left alone.
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
		for _, resource := range s.TemplateResources() {
			body, err := s.ReadResource(resource)
			if err != nil {
				problems = append(problems, fmt.Sprintf("skill %q procedure %q: %v", s.Name, resource, err))
				continue
			}
			bodies[s.Name] += "\n" + string(body)
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
			optional := optionalToolSet(skill)
			for _, tool := range NamedTools(c, bodies[name]) {
				if optional[tool] || c.ProfileHolds(agent.ProfileID, tool) {
					continue
				}
				problems = append(problems, fmt.Sprintf(
					"skill %q tells its reader to call %q, but agent %q runs on profile %q, which does not hold it — "+
						"grant the tool, drop the skill from that agent, or list the name under optional_tools",
					skill.Name, tool, agent.AgentID, agent.ProfileID))
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

func optionalToolSet(s skills.Skill) map[string]bool {
	out := make(map[string]bool, len(s.OptionalTools))
	for _, name := range s.OptionalTools {
		out[strings.TrimSpace(name)] = true
	}
	return out
}
