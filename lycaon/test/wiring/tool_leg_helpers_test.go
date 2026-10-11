package wiring

import (
	"context"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func compositeWorkerWithPolicy(agents orchestration.AgentRegistry, policy interface {
	ListForPrompt(context.Context, *api.Session, string) []tools.ToolMeta
}) *delegation.CompositeWorkerContext {
	return &delegation.CompositeWorkerContext{
		AgentsFor: func(*api.Session) profiles.AgentProfileResolver { return agents },
		Tools: delegation.LegToolListerFunc(func(c context.Context, s *api.Session, profileID string) []string {
			return sortedToolNamesFromMeta(policy.ListForPrompt(c, s, profileID))
		}),
	}
}

func sortedToolNamesFromMeta(metas []tools.ToolMeta) []string {
	names := make([]string, 0, len(metas))
	for _, meta := range metas {
		if strings.TrimSpace(meta.Name) != "" {
			names = append(names, meta.Name)
		}
	}
	sort.Strings(names)
	return names
}

func legToolsContains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
