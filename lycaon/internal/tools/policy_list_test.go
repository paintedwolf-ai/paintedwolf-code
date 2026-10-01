package tools_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestProfilePolicyEngineEvaluateForListHidesRuntimeDenied(t *testing.T) {
	boundary := sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{{
		ID:    "implement",
		Tools: map[string]bool{"web_search": true, "fetch_url": true, "read": true},
	}})
	policy := tools.NewProfilePolicyEngine(boundary)
	policy.SetRuntimeToolDeny(func(toolName string) bool {
		return toolName == "web_search" || toolName == "fetch_url"
	})

	for _, name := range []string{"web_search", "fetch_url"} {
		decision, err := policy.EvaluateForList(context.Background(), platform.PolicyContext{
			ProfileID: "implement",
			ToolName:  name,
		})
		testutil.FailErr(t, "policy.EvaluateForList failed", err)
		if decision.Allowed || decision.Blocked {
			t.Fatalf("%s list decision = %+v want hidden", name, decision)
		}
	}
	read, err := policy.EvaluateForList(context.Background(), platform.PolicyContext{
		ProfileID: "implement",
		ToolName:  "read",
	})
	testutil.FailErr(t, "policy.EvaluateForList failed", err)
	if !read.Allowed || read.Blocked {
		t.Fatalf("read list decision = %+v", read)
	}
}

func TestListToolsForProfileOmitsRuntimeDeniedWebResearch(t *testing.T) {
	boundary := sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{{
		ID:    "implement",
		Tools: map[string]bool{"web_search": true, "fetch_url": true, "read": true},
	}})
	engine := tools.NewProfilePolicyEngine(boundary)
	engine.SetRuntimeToolDeny(func(toolName string) bool {
		return toolName == "web_search" || toolName == "fetch_url"
	})
	invoker := &listPolicyInvoker{
		metas:  []tools.ToolMeta{{Name: "read"}, {Name: "web_search"}, {Name: "fetch_url"}},
		policy: tools.NewGuidanceRejectPolicy(engine),
	}
	got := tools.ListToolsForProfile(context.Background(), invoker, platform.ToolFilter{ProfileID: "implement"})
	if len(got) != 1 || got[0].Name != "read" {
		t.Fatalf("listed = %+v want read only", got)
	}
}

type listPolicyInvoker struct {
	metas  []tools.ToolMeta
	policy platform.PolicyEngine
}

func (l *listPolicyInvoker) Invoke(context.Context, string, map[string]any, tools.ToolContext) (string, error) {
	return "", nil
}

func (l *listPolicyInvoker) List(ctx context.Context, filter platform.ToolFilter) ([]tools.ToolMeta, error) {
	out := make([]tools.ToolMeta, 0, len(l.metas))
	for _, meta := range l.metas {
		decision, err := tools.EvaluateListVisible(ctx, l.policy, platform.PolicyContext{
			ProfileID: filter.ProfileID,
			ToolName:  meta.Name,
		})
		if err != nil {
			continue
		}
		if decision != nil && decision.Allowed {
			out = append(out, meta)
		}
	}
	return out, nil
}
