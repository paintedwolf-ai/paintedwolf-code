package promptloop

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAskFinalPlanClampsLoadedAndImpliedCalls(t *testing.T) {
	metas := []tools.ToolMeta{
		{Name: "inspect_source"}, {Name: "command"}, {Name: "command_stop"}, {Name: "request_tools"},
		{Name: "mcp_doc_lookup", Source: tools.ToolSourceMCP, ReadOnlyHint: true},
		{Name: "mcp_doc_update", Source: tools.ToolSourceMCP},
		{Name: "workflow_advance"}, {Name: "unknown_dynamic"},
	}
	loop := NewPromptLoopForTest(PromptLoopDeps{Context: ContextDeps{
		Policy: registryTestPolicy{metas: metas},
		LoadedTools: func(string) map[string]bool {
			return map[string]bool{"mcp_doc_lookup": true, "mcp_doc_update": true, "unknown_dynamic": true}
		},
		LiveResources: func(string) toolcontract.ResourcePresence { return toolcontract.ResourcePresence{CommandJobs: true} },
	}})
	for _, posture := range []api.SessionPosture{api.SessionPostureSpec, api.SessionPostureBuild, api.SessionPostureOrchestrate, api.SessionPostureVet} {
		sess := &api.Session{ID: "session", Mode: api.SessionModeAsk, Posture: posture, WorkspacePath: "/project", AgentType: "coordinator"}
		frame := inject.CoordinatorTurnFrame{RunContext: api.CoordinatorRunContext{AdvanceWhenGateMet: "coordinator"}}
		meta, plan := mustCoordinatorToolsForTurn(t, loop, sess, frame)
		for _, name := range []string{"inspect_source", "mcp_doc_lookup", "request_tools", "workflow_advance"} {
			if !containsMeta(meta, name) {
				t.Errorf("%s: missing %s", posture, name)
			}
		}
		for _, name := range []string{"command", "command_stop", "mcp_doc_update", "unknown_dynamic"} {
			if plan.Addressable(name) {
				t.Errorf("%s: prohibited %s is addressable", posture, name)
			}
		}
	}
}

func TestRoundAuthorityKeepsWorkerScopeAcrossUpgradeAndFailsClosed(t *testing.T) {
	root := store.RootAuthority{RootSessionID: "root", Mode: api.SessionModeAsk, Started: true}
	var readErr error
	prompt := &promptContext{Deps: ContextDeps{ResolveEffectiveMode: func(context.Context, string) (store.RootAuthority, error) {
		return root, readErr
	}}}
	worker := &api.Session{ID: "worker", ParentSessionID: "root", Mode: api.SessionModeAsk}
	ask, err := prompt.roundAuthority(t.Context(), worker, true)
	testutil.FailErr(t, "read Ask round", err)
	if tc := ask.toolContext(tools.ToolContext{}); !tc.Source.AskMode || !tc.Source.ReadOnly || tc.Identity.RootSessionID != "root" {
		t.Fatalf("Ask round flags = %+v", tc.Source)
	}
	root.Mode = api.SessionModeAgent
	agent, err := prompt.roundAuthority(t.Context(), worker, true)
	testutil.FailErr(t, "read Agent round", err)
	if tc := agent.toolContext(tools.ToolContext{}); tc.Source.AskMode || !tc.Source.ReadOnly {
		t.Fatalf("Agent worker flags = %+v", tc.Source)
	}
	readErr = errors.New("root unavailable")
	if _, err := prompt.roundAuthority(t.Context(), worker, true); !errors.Is(err, readErr) {
		t.Fatalf("round authority error = %v", err)
	}
}
