package orchestration

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// MaxTeamAgents limits a supervised team.
const MaxTeamAgents = 5

// SkillsReadTool is the tool that opens an agent's skill index.
const SkillsReadTool = "skills_read"

// Bundled agent profile IDs.
const (
	ProfileCoordinator      = "coordinator"
	ProfileImplementer      = "implementer"
	ProfileRepoResearcher   = "repo-researcher"
	ProfilePathExplorer     = "path-explorer"
	ProfileCodeReviewer     = "code-reviewer"
	ProfileWebResearcher    = "web-researcher"
	ProfileSecurityReviewer = "security-reviewer"
)

// Team is a composed group of agents working on one task.
type Team struct {
	ID       string
	Task     string
	Strategy TeamStrategy
	Members  []TeamMember
}

// TeamMember binds a profile to a runtime agent instance.
type TeamMember struct {
	AgentID   string
	ProfileID string
}

// AgentRegistry registers agent profiles from config.
type AgentRegistry interface {
	LoadFromDir(ctx context.Context, dir extpacks.Source) error
	Register(profile agentdef.Profile) error
	Get(id string) (agentdef.Profile, error)
	List() []agentdef.Profile
	Compose(strategy TeamStrategy, profileIDs []string) (*Team, error)
	ResolveForGate(gate evidence.GateType) (agentdef.Profile, error)
}

// LoadRequiredAgentRegistry loads all contributing agent profiles.
func LoadRequiredAgentRegistry(_ context.Context, reg *MemoryAgentRegistry) error {
	if reg == nil {
		return fmt.Errorf("agent registry required")
	}
	profiles, err := agentdef.LoadEffective()
	if err != nil {
		return err
	}
	for _, p := range profiles {
		if err := reg.Register(p); err != nil {
			return err
		}
	}
	if len(profiles) == 0 {
		return fmt.Errorf("agents: no profiles loaded")
	}
	return nil
}

// ValidateAgentToolProfiles ensures each loaded agent references a bundled tool profile id.
func ValidateAgentToolProfiles(reg AgentRegistry, profiles []sandbox.ToolProfile) error {
	if reg == nil {
		return fmt.Errorf("agent registry required")
	}
	known := make(map[string]struct{}, len(profiles))
	for _, p := range profiles {
		if p.ID != "" {
			known[p.ID] = struct{}{}
		}
	}
	for _, agent := range reg.List() {
		tp := strings.TrimSpace(agent.ToolProfile)
		if tp == "" {
			return fmt.Errorf("agent %q: missing tool_profile", agent.ID)
		}
		if _, ok := known[tp]; !ok {
			return fmt.Errorf("agent %q: tool_profile %q not found", agent.ID, tp)
		}
	}
	return nil
}

// ValidateAgentSkillSurface ensures an agent that selects skills holds the tool
// that reads them, or its roster would list skills that reject on call. The
// converse is fine: turn assembly hides skills_read when the index is empty.
func ValidateAgentSkillSurface(reg AgentRegistry, profiles map[string]sandbox.ToolProfile) error {
	if reg == nil {
		return fmt.Errorf("agent registry required")
	}
	for _, agent := range reg.List() {
		tp := strings.TrimSpace(agent.ToolProfile)
		prof, ok := profiles[tp]
		if !ok {
			return fmt.Errorf("agent %q: tool_profile %q not found", agent.ID, tp)
		}
		if !agent.Skills.Empty() && !prof.ToolAllowed(SkillsReadTool) {
			return fmt.Errorf("agent %q selects skills but profile %q does not grant %s",
				agent.ID, tp, SkillsReadTool)
		}
	}
	return nil
}
