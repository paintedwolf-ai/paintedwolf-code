package wiring

import (
	"context"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
)

func CompositeWorkerWithPolicy(agents orchestration.AgentRegistry, policy interface {
	ListForPrompt(context.Context, *api.Session, string) []tools.ToolMeta
}) *workercontext.CompositeWorkerContext {
	return &workercontext.CompositeWorkerContext{
		AgentsFor: func(*api.Session) profiles.AgentProfileResolver { return agents },
		Tools: workercontext.LegToolListerFunc(func(c context.Context, s *api.Session, profileID string) []string {
			return SortedToolNamesFromMeta(policy.ListForPrompt(c, s, profileID))
		}),
	}
}

func SortedToolNamesFromMeta(metas []tools.ToolMeta) []string {
	names := make([]string, 0, len(metas))
	for _, meta := range metas {
		if strings.TrimSpace(meta.Name) != "" {
			names = append(names, meta.Name)
		}
	}
	sort.Strings(names)
	return names
}

func LegToolsContains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
