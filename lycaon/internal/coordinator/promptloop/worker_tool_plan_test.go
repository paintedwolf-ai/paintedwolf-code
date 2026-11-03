package promptloop

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/argdiag"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerPlanPromotesOnlyPermittedResourcesAndActivations(t *testing.T) {
	loop := NewPromptLoop(PromptLoopDeps{
		Context: ContextDeps{
			LiveResources: func(string) toolcontract.ResourcePresence {
				return toolcontract.ResourcePresence{Pages: true, CommandJobs: true, Terminals: true}
			},
		},
	})
	metas := []tools.ToolMeta{{Name: "read"}, {Name: "request_tools"}, {Name: "page_open", Deferred: true}, {Name: "page_act", Deferred: true}, {Name: "page_close", Deferred: true}, {Name: "command_output", Deferred: true}, {Name: "terminal_read", Deferred: true}, {Name: "scan_query", Deferred: true}}
	plan := loop.Context.compileWorkerToolPlan(t.Context(), &api.Session{ID: "worker"}, metas, map[string]bool{"scan_query": true, "git_commit": true})
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
	loop := NewPromptLoop(PromptLoopDeps{
		Context: ContextDeps{
			ContextSurfaceDeps: ContextSurfaceDeps{
				WebSearchEnabled: func() bool { return false },
			},
		},
	})
	metas := []tools.ToolMeta{{Name: "read"}, {Name: "request_tools"}, {Name: "web_search", Deferred: true}, {Name: "fetch_url", Deferred: true}}
	plan := loop.Context.compileWorkerToolPlan(t.Context(), &api.Session{ID: "worker"}, metas, map[string]bool{"web_search": true, "fetch_url": true})
	if !plan.Immediate("read") || !plan.Immediate("request_tools") || plan.Addressable("web_search") || plan.Addressable("fetch_url") {
		t.Fatalf("disabled research remained reachable: %v", plan.AddressableNames())
	}
}

// TestTaskSpawnAllowlistForTurnNilVsEmpty pins the nil-vs-empty distinction taskSpawnAllowlistForTurn
// carries: nil when the frame has no roster (declared fallback applies), empty non-nil when the
// roster filtered every agent out — the repoKnownEmpty scenario, where nothing is dispatchable.
func TestTaskSpawnAllowlistForTurnNilVsEmpty(t *testing.T) {
	if got := taskSpawnAllowlistForTurn(inject.CoordinatorTurnFrame{}); got != nil {
		t.Fatalf("no roster must yield nil, got %#v", got)
	}

	filtered := inject.CoordinatorTurnFrame{Roster: &inject.AgentRoster{
		Declared: []string{"implementer", "repo-researcher"},
		Excluded: []inject.ExcludedAgent{
			{Name: "implementer", Code: inject.RosterExcludeRepoEmpty},
			{Name: "repo-researcher", Code: inject.RosterExcludeRepoEmpty},
		},
	}}
	got := taskSpawnAllowlistForTurn(filtered)
	if got == nil || len(got) != 0 {
		t.Fatalf("all-excluded roster must yield empty non-nil, got %#v", got)
	}

	populated := inject.CoordinatorTurnFrame{Roster: &inject.AgentRoster{
		Declared:  []string{"implementer"},
		Effective: []string{"implementer"},
	}}
	if got := taskSpawnAllowlistForTurn(populated); len(got) != 1 || got[0] != "implementer" {
		t.Fatalf("effective roster lost: %#v", got)
	}
}

func TestTaskSpawnCommitted(t *testing.T) {
	if !taskSpawnCommitted("task", &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Dispatch: &api.WorkerDispatch{WorkerID: "j"}}, true) {
		t.Fatal("expected committed")
	}
	if taskSpawnCommitted("task", &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted}, true) {
		t.Fatal("expected not committed")
	}
	if taskSpawnCommitted("read", &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Dispatch: &api.WorkerDispatch{WorkerID: "j"}}, true) {
		t.Fatal("expected not committed for read")
	}
}

func TestToolRejectMessageDoesNotInferEnqueuedJobID(t *testing.T) {
	loop := NewPromptLoop(PromptLoopDeps{})
	body := `{"agent_type":"skeptic","job_id":"job-sk","status":"enqueued"}
>>> Worker queued
Code: BANNER_TASK_QUEUED

>>> Tool feedback
Workflow gate blocked at phase adjudicate
Code: WORKFLOW_GATE_BLOCKED`
	msg := loop.Tools.toolRejectMessage("task", "call-1", "asst-1", map[string]any{"agent_type": "skeptic"},
		guidance.NewRefusal("WORKFLOW_GATE_BLOCKED", body))
	if msg.ToolResult == nil {
		t.Fatal("missing tool_result")
	}
	if msg.ToolResult.Dispatch != nil {
		t.Fatalf("dispatch must not be inferred from content: %+v", msg.ToolResult)
	}
	if msg.ToolResult.Outcome != api.ToolResultOutcomeRejected {
		t.Fatalf("outcome = %q want rejected", msg.ToolResult.Outcome)
	}
}

func TestLiveProjectedToolCallsKeepsNamedLongRunningOnly(t *testing.T) {
	got := liveProjectedToolCalls([]api.ToolCall{
		{ID: "c1", Name: "command", Args: map[string]any{"command": "./task check-fast"}},
		{ID: "r1", Name: "read", Args: map[string]any{"path": "a.go"}},
		{ID: "", Name: "verify"},
		{Name: "summarize", Args: map[string]any{"path": "pkg"}},
		{ID: "v1", Name: "verify"},
	})
	if len(got) != 2 {
		t.Fatalf("liveProjectedToolCalls = %+v want command + verify", got)
	}
	if got[0].ID != "c1" || got[0].Name != "command" {
		t.Fatalf("first = %+v want command c1", got[0])
	}
	if got[1].ID != "v1" || got[1].Name != "verify" {
		t.Fatalf("second = %+v want verify v1", got[1])
	}
}

// A worker dispatch settles on ui_visibility: a refused batch comes back benign.
func TestLiveProjectedToolCallsWithholdsWorkerDispatch(t *testing.T) {
	got := liveProjectedToolCalls([]api.ToolCall{
		{ID: "t1", Name: "task", Args: map[string]any{"agent_type": "path-explorer"}},
		{ID: "d1", Name: "delegate_dispatch", Args: map[string]any{"brief": "leg"}},
	})
	if got != nil {
		t.Fatalf("liveProjectedToolCalls = %+v want no worker-dispatch projection", got)
	}
}

func TestLiveProjectedToolCallsEmpty(t *testing.T) {
	if liveProjectedToolCalls(nil) != nil {
		t.Fatal("nil calls should project nothing")
	}
	if liveProjectedToolCalls([]api.ToolCall{{ID: "r1", Name: "read"}}) != nil {
		t.Fatal("deferred tools must not project")
	}
}

func TestToolSchemaOrderIsStableAcrossCatalogEnumeration(t *testing.T) {
	a := tools.ToolMeta{Name: "read", ArgsSchema: map[string]any{"type": "object"}}
	b := tools.ToolMeta{Name: "command", ArgsSchema: map[string]any{"type": "object"}}
	plan := toolsurface.Compile([]string{"read", "command"}, nil)
	for _, project := range []func([]tools.ToolMeta) []tools.ToolMeta{
		trimPromptToolMetas,
		func(metas []tools.ToolMeta) []tools.ToolMeta {
			return trimCoordinatorToolMetasForPlan(plan, metas, nil, nil)
		},
	} {
		first, err := json.Marshal(project([]tools.ToolMeta{a, b}))
		if err != nil {
			t.Fatalf("marshal tool schemas: %v", err)
		}
		second, err := json.Marshal(project([]tools.ToolMeta{b, a}))
		if err != nil {
			t.Fatalf("marshal tool schemas: %v", err)
		}
		if string(first) != string(second) {
			t.Fatalf("schema order depends on catalog enumeration: %s != %s", first, second)
		}
	}
}

func TestReviewPhaseOffersItsVerdictSchema(t *testing.T) {
	catalog := map[string]any{"type": "object", "properties": map[string]any{"verdict": map[string]any{"type": "object"}}}
	phase := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{"verdict": map[string]any{
			"type":     "object",
			"required": []any{"verdict"},
			"properties": map[string]any{
				"verdict": map[string]any{"type": "string", "enum": []any{"CLAIMED"}},
			},
		}},
	}
	metas := []tools.ToolMeta{{Name: "submit_verdict", ArgsSchema: catalog}, {Name: "read", ArgsSchema: map[string]any{"type": "object"}}}
	frame := inject.CoordinatorTurnFrame{Runtime: inject.WorkflowRuntimeSnapshot{PhaseExit: &inject.PhaseExitView{SubmitVerdictArgsSchema: phase}}}

	offered := trimCoordinatorToolMetasForPlan(toolsurface.Compile([]string{"submit_verdict", "read"}, nil), metas, nil, phaseVerdictSchema(frame))
	var verdict tools.ToolMeta
	for _, meta := range offered {
		if meta.Name == "submit_verdict" {
			verdict = meta
		}
	}
	props, _ := verdict.ArgsSchema["properties"].(map[string]any)
	member, _ := props["verdict"].(map[string]any)
	if got := argdiag.Outline(member); got != "{verdict: CLAIMED}" {
		t.Fatalf("offered verdict member = %s, want the phase schema", got)
	}
	if _, ok := metas[0].ArgsSchema["additionalProperties"]; ok {
		t.Fatal("offering the phase schema mutated the catalog meta")
	}
	if outside := phaseVerdictSchema(inject.CoordinatorTurnFrame{}); outside != nil {
		t.Fatal("a turn outside a review phase got a phase verdict schema")
	}
}
