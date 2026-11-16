package orchestration

import (
	"context"
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/extpacks"
)

// MemoryAgentRegistry is an in-memory AgentRegistry for profile resolution.
type MemoryAgentRegistry struct {
	mu     sync.RWMutex
	agents map[string]agentdef.Profile
}

// NewMemoryAgentRegistry creates an empty registry that boot fills from agent YAML.
func NewMemoryAgentRegistry() *MemoryAgentRegistry {
	return &MemoryAgentRegistry{
		agents: make(map[string]agentdef.Profile),
	}
}

// NewMemoryAgentRegistryForTest seeds built-in defaults for unit tests without bundled YAML.
func NewMemoryAgentRegistryForTest() *MemoryAgentRegistry {
	r := NewMemoryAgentRegistry()
	for _, p := range defaultAgentProfiles() {
		_ = r.Register(p)
	}
	return r
}

func defaultAgentProfiles() []agentdef.Profile {
	return []agentdef.Profile{
		{ID: ProfileCoordinator, ToolProfile: "coordinator"},
		{ID: ProfileImplementer, ToolProfile: "implement"},
		{ID: ProfileRepoResearcher, ToolProfile: "explore_readonly"},
		{ID: ProfileCodeReviewer, ToolProfile: "explore_readonly"},
		{ID: ProfilePathExplorer, ToolProfile: "explore_readonly"},
	}
}

// LoadFromDir loads agent YAML profiles from dir.
func (r *MemoryAgentRegistry) LoadFromDir(_ context.Context, dir extpacks.Source) error {
	profiles, err := agentdef.LoadDir(dir)
	if err != nil {
		return err
	}
	for _, p := range profiles {
		if err := r.Register(p); err != nil {
			return err
		}
	}
	return nil
}

// Register adds or replaces an agent profile.
func (r *MemoryAgentRegistry) Register(profile agentdef.Profile) error {
	if profile.ID == "" {
		return fmt.Errorf("agent profile missing id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[profile.ID] = profile
	return nil
}

// Get returns a profile by id.
func (r *MemoryAgentRegistry) Get(id string) (agentdef.Profile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if id == "" {
		return agentdef.Profile{}, fmt.Errorf("agent id required")
	}
	if p, ok := r.agents[id]; ok {
		return p, nil
	}
	return agentdef.Profile{}, fmt.Errorf("agent profile %q not found", id)
}

// List returns all registered profiles.
func (r *MemoryAgentRegistry) List() []agentdef.Profile {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]agentdef.Profile, 0, len(r.agents))
	for _, p := range r.agents {
		out = append(out, p)
	}
	return out
}

// Compose builds a team from profile ids.
func (r *MemoryAgentRegistry) Compose(strategy TeamStrategy, profileIDs []string) (*Team, error) {
	if len(profileIDs) == 0 {
		return nil, fmt.Errorf("profile ids required")
	}
	if len(profileIDs) > MaxTeamAgents {
		return nil, fmt.Errorf("team exceeds max agents (%d)", MaxTeamAgents)
	}
	members := make([]TeamMember, 0, len(profileIDs))
	for _, id := range profileIDs {
		if _, err := r.Get(id); err != nil {
			return nil, err
		}
		members = append(members, TeamMember{AgentID: id, ProfileID: id})
	}
	return &Team{Strategy: strategy, Members: members}, nil
}

// ValidateGateAgents ensures every GateRecommendedAgents id is registered.
func ValidateGateAgents(r AgentRegistry) error {
	if r == nil {
		return fmt.Errorf("agent registry required")
	}
	for gate, agentID := range evidence.GateRecommendedAgents() {
		if _, err := r.Get(agentID); err != nil {
			return fmt.Errorf("gate %q: agent %q: %w", gate, agentID, err)
		}
	}
	return nil
}

// ResolveForGate returns a profile for an evidence gate type.
func (r *MemoryAgentRegistry) ResolveForGate(gate evidence.GateType) (agentdef.Profile, error) {
	rec, ok := evidence.GateRecommendedAgent(gate)
	if !ok {
		return agentdef.Profile{}, fmt.Errorf("no agent for gate %q", gate)
	}
	return r.Get(rec)
}
