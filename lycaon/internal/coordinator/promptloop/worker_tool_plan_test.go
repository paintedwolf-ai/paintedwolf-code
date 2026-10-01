package promptloop

import (
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerPlanPromotesOnlyPermittedResourcesAndActivations(t *testing.T) {
	loop := &PromptLoop{Deps: PromptLoopDeps{LiveResources: func(string) toolcontract.ResourcePresence {
		return toolcontract.ResourcePresence{Pages: true, CommandJobs: true, Terminals: true}
	}}}
	metas := []tools.ToolMeta{{Name: "read"}, {Name: "request_tools"}, {Name: "page_open", Deferred: true}, {Name: "page_act", Deferred: true}, {Name: "page_close", Deferred: true}, {Name: "command_output", Deferred: true}, {Name: "terminal_read", Deferred: true}, {Name: "scan_query", Deferred: true}}
	plan := loop.compileWorkerToolPlan(t.Context(), &api.Session{ID: "worker"}, metas, map[string]bool{"scan_query": true, "git_commit": true})
	for _, name := range []string{"read", "request_tools", "page_act", "page_close", "command_output", "terminal_read", "scan_query"} {
		if !plan.Immediate(name) {
			t.Fatalf("missing eligible tool %s", name)
		}
	}
	if !plan.Deferred("page_open") {
		t.Fatal("resource promoted its opener")
	}
	for _, name := range []string{"git_commit", "workflow_advance", "page_snapshot", "command_stop"} {
		if plan.Addressable(name) {
			t.Fatalf("promoted unavailable tool %s", name)
		}
	}
}

func TestWorkerPlanDoesNotRestoreDisabledResearch(t *testing.T) {
	loop := &PromptLoop{Deps: PromptLoopDeps{WebSearchEnabled: func() bool { return false }}}
	metas := []tools.ToolMeta{{Name: "read"}, {Name: "request_tools"}, {Name: "web_search", Deferred: true}, {Name: "fetch_url", Deferred: true}}
	plan := loop.compileWorkerToolPlan(t.Context(), &api.Session{ID: "worker"}, metas, map[string]bool{"web_search": true, "fetch_url": true})
	if !plan.Immediate("read") || !plan.Immediate("request_tools") || plan.Addressable("web_search") || plan.Addressable("fetch_url") {
		t.Fatalf("disabled research remained reachable: %v", plan.AddressableNames())
	}
}
