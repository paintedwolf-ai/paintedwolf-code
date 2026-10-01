package inject

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
)

// Roster exclusion codes match dispatch rejection codes.
const (
	RosterExcludeNotAWorker   = "NOT_A_WORKER"
	RosterExcludeLane         = "AGENT_LANE_MISMATCH"
	RosterExcludeNoWorkspace  = "NO_WORKSPACE_ROOTS"
	RosterExcludeRepoEmpty    = "REPO_EMPTY_READ_ONLY_WORKER"
	RosterExcludeWebSearchOff = "WEB_SEARCH_DISABLED"
)

// ExcludedAgent records one unavailable declared agent and why. The roster
// renders the reason beside the code.
type ExcludedAgent struct {
	Name   string
	Code   string
	Reason string
}

// AgentRoster is one turn's agent availability.
type AgentRoster struct {
	Facts     RosterFacts
	Declared  []string
	Effective []string
	Excluded  []ExcludedAgent
}

// RosterFacts are the measured conditions shared by availability and rendering.
type RosterFacts struct {
	SurfaceID        string
	RootCount        int
	RepoKnownEmpty   bool
	WebSearchEnabled bool
}

// ResolveAgentRoster applies availability filters once per turn. Every filter is
// a predicate over facts the agent declares — topology role, dispatch lanes, and
// the tool surface its profile grants — never over agent ids, so pack-installed
// and bundled agents are admitted on the same terms.
func ResolveAgentRoster(surfaceID string, declared []string, rootCount int, repoKnownEmpty, webSearchEnabled bool) AgentRoster {
	facts := RosterFacts{
		SurfaceID:        surfaceID,
		RootCount:        rootCount,
		RepoKnownEmpty:   repoKnownEmpty,
		WebSearchEnabled: webSearchEnabled,
	}
	roster := AgentRoster{Facts: facts}
	for _, name := range declared {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		roster.Declared = append(roster.Declared, name)
		if excluded, ok := excludeAgent(name, facts); ok {
			roster.Excluded = append(roster.Excluded, excluded)
			continue
		}
		roster.Effective = append(roster.Effective, name)
	}
	return roster
}

// excludeAgent returns the first reason one agent is not dispatchable this turn.
func excludeAgent(name string, facts RosterFacts) (ExcludedAgent, bool) {
	exclude := func(code, reason string) (ExcludedAgent, bool) {
		return ExcludedAgent{Name: name, Code: code, Reason: reason}, true
	}
	if !agentdef.IsWorker(name) {
		return exclude(RosterExcludeNotAWorker,
			"declares no `worker` topology role, so it is not a task() target")
	}
	if lane, gated := spawn.LaneForSurface(facts.SurfaceID); gated && !agentdef.ServesLane(name, lane) {
		return exclude(RosterExcludeLane, fmt.Sprintf(
			"this turn dispatches the %s lane; the agent declares %s",
			lane, describeLanes(name)))
	}
	if facts.RootCount == 0 && !prompts.AgentRunsWithoutWorkspace(name) {
		return exclude(RosterExcludeNoWorkspace,
			"no workspace root is open and the agent reads or writes the project tree")
	}
	if facts.RepoKnownEmpty && !prompts.KeepSpawnAgentOnEmptyRepo(name) {
		return exclude(RosterExcludeRepoEmpty,
			"the workspace is measured empty and the agent only surveys the tree")
	}
	if !facts.WebSearchEnabled && agentdef.DeclaresAny(name, agentdef.CapabilityExternal) {
		return exclude(RosterExcludeWebSearchOff,
			"web search is off and the agent works from external sources")
	}
	return ExcludedAgent{}, false
}

// describeLanes renders an agent's declared lanes for an exclusion reason.
func describeLanes(name string) string {
	lanes := agentdef.LanesFor(name)
	if len(lanes) == 0 {
		return "none"
	}
	return strings.Join(lanes, ", ")
}

// ExcludedAgentRows maps exclusions to template rows.
func ExcludedAgentRows(excluded []ExcludedAgent) []map[string]any {
	if len(excluded) == 0 {
		return nil
	}
	rows := make([]map[string]any, len(excluded))
	for i, ex := range excluded {
		rows[i] = map[string]any{"name": ex.Name, "code": ex.Code, "reason": ex.Reason}
	}
	return rows
}
