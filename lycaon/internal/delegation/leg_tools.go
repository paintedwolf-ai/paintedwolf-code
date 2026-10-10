package delegation

import (
	"context"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/pkg/api"
)

// LegToolLister returns prompt-visible tool names for a worker child session.
type LegToolLister interface {
	ListLegTools(ctx context.Context, sess *api.Session, profileID string) []string
}

// LegToolListerFunc adapts a function to LegToolLister.
type LegToolListerFunc func(ctx context.Context, sess *api.Session, profileID string) []string

func (f LegToolListerFunc) ListLegTools(ctx context.Context, sess *api.Session, profileID string) []string {
	if f == nil {
		return nil
	}
	return f(ctx, sess, profileID)
}

func resolveLegTools(
	ctx context.Context,
	lister LegToolLister,
	agents profiles.AgentProfileResolver,
	sess *api.Session,
	agentType string,
) []string {
	agentType = strings.TrimSpace(agentType)
	if agentType == "" || agents == nil || sess == nil {
		return nil
	}
	prof, err := agents.Get(agentType)
	if err != nil {
		return nil
	}
	profileID := strings.TrimSpace(prof.ToolProfile)
	if profileID == "" {
		return nil
	}
	if lister == nil {
		return nil
	}
	return sortedToolNames(lister.ListLegTools(ctx, sess, profileID))
}

func playbookPhaseForTaskSpawn(agentType, phaseID string) string {
	if strings.TrimSpace(phaseID) != "" {
		return phaseID
	}
	if agentType == orchestration.ProfileImplementer {
		return "implement"
	}
	return phaseID
}

func mergeTaskSpawnChecklist(
	matcher PlaybookMatcherInterface,
	agents profiles.AgentProfileResolver,
	agentType, topology string,
) ([]string, error) {
	if matcher == nil {
		return nil, nil
	}
	phase := playbookPhaseForTaskSpawn(agentType, "")
	return matcher.MatchForAgent(agentType, topology, phase)
}

// PlaybookMatcherInterface is the playbook subset used for worker leg checklists.
type PlaybookMatcherInterface interface {
	MatchForAgent(agentID, topologyPattern, phaseID string) ([]string, error)
}

func sortedToolNames(names []string) []string {
	out := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
