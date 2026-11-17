package workercontext

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

// PlaybookMatcherInterface resolves playbook checklists for a worker profile.
type PlaybookMatcherInterface interface {
	MatchForAgent(agentID, topologyPattern, phaseID string) ([]string, error)
}

func resolveLegTools(
	ctx context.Context,
	lister LegToolLister,
	agents profiles.AgentProfileResolver,
	sess *api.Session,
	agentType string,
) []string {
	agentType = strings.TrimSpace(agentType)
	if agentType == "" || agents == nil || sess == nil || lister == nil {
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
	return sortedToolNames(lister.ListLegTools(ctx, sess, profileID))
}

func playbookPhaseForWorker(agentType, phaseID string) string {
	if strings.TrimSpace(phaseID) != "" {
		return phaseID
	}
	if agentType == orchestration.ProfileImplementer {
		return "implement"
	}
	return phaseID
}

func mergeWorkerChecklist(
	matcher PlaybookMatcherInterface,
	agentType, topology, phaseID string,
) ([]string, error) {
	if matcher == nil {
		return nil, nil
	}
	return matcher.MatchForAgent(agentType, topology, playbookPhaseForWorker(agentType, phaseID))
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
