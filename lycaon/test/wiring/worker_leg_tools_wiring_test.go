package wiring

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

var implementDefaultAgents = []string{
	orchestration.ProfileImplementer,
	orchestration.ProfileRepoResearcher,
	orchestration.ProfilePathExplorer,
	orchestration.ProfileCodeReviewer,
}

func TestWorkerLegToolsMatchListForPrompt(t *testing.T) {
	h := BuildForTest(t, WithDecider(decide.Absent{}))
	ctx := context.Background()
	dir := t.TempDir()
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	policy := h.Sessions.Manager.Coordinator.Guards.Policy()
	builder := compositeWorkerWithPolicy(h.AgentRegistry, policy)

	for _, agentType := range implementDefaultAgents {
		t.Run(agentType, func(t *testing.T) {
			child, err := h.Sessions.Manager.Workers.SpawnChild(ctx, sess.ID, api.SpawnChildRequest{
				AgentType: agentType,
				Prompt:    "survey",
			})
			testutil.FailErr(t, "SpawnChild", err)
			prof, err := h.AgentRegistry.Get(agentType)
			testutil.FailErr(t, "agents.Get", err)
			schema := sortedToolNamesFromMeta(policy.ListForPrompt(ctx, child, prof.ToolProfile))
			leg, err := builder.BuildWorkerPromptContext(child.ID, child)
			testutil.FailErr(t, "BuildWorkerPromptContext", err)
			got := append([]string(nil), leg.LegTools...)
			sort.Strings(got)
			if !reflect.DeepEqual(got, schema) {
				t.Fatalf("LegTools %v != schema %v", got, schema)
			}
			if len(got) == 0 {
				t.Fatal("LegTools must not be empty when profile has tools")
			}
			if agentType == orchestration.ProfileImplementer && !legToolsContains(got, "command") {
				t.Fatalf("implementer LegTools must include command: %v", got)
			}
			if agentType == orchestration.ProfilePathExplorer && !legToolsContains(got, "find") {
				t.Fatalf("path-explorer LegTools missing find: %v", got)
			}
		})
	}
}

// implementerLegToolsWant is the isolated implementation profile.
var implementerLegToolsWant = []string{
	"capture_page", "chmod", "chown", "code_rewrite", "command", "command_output", "command_stop", "complete_leg", "copy", "delete", "diff", "edit", "extract_archive", "fetch_url", "find", "grep", "handoff_release", "handoff_release_all", "handoff_reserve", "held_result", "held_stop", "http_request", "jq", "jq_edit", "list_dir", "measure_page", "mkdir", "move", "pack_board", "page_act", "page_close", "page_open", "page_snapshot", "process_list", "process_signal", "read", "recall", "record_finding", "render_view", "replace_lines", "request_budget", "request_decision", "request_tools", "restore_version", "scan_compare", "scan_list", "scan_pack", "scan_query", "scan_summary", "secret_generate", "secret_list", "secret_revoke", "skills_read", "source_history", "stat", "summarize", "survey_repo", "terminal_close", "terminal_open", "terminal_read", "terminal_send", "terminal_snapshot", "verify", "view_image", "view_video", "wait", "wc", "web_search", "write",
}

func TestImplementerLegToolsMatchImplementProfile(t *testing.T) {
	fixture := struct {
		AgentType    string
		ProfileID    string
		WantLegTools []string
	}{
		AgentType:    orchestration.ProfileImplementer,
		ProfileID:    "implement",
		WantLegTools: implementerLegToolsWant,
	}
	h := BuildForTest(t, WithDecider(decide.Absent{}))
	ctx := context.Background()
	dir := t.TempDir()
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create parent", err)
	child, err := h.Sessions.Manager.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: fixture.AgentType,
		Prompt:    "implement scaffold",
	})
	testutil.FailErr(t, "SpawnChild", err)
	policy := h.Sessions.Manager.Coordinator.Guards.Policy()
	schema := sortedToolNamesFromMeta(policy.ListForPrompt(ctx, child, fixture.ProfileID))
	want := append([]string(nil), fixture.WantLegTools...)
	sort.Strings(want)
	if !reflect.DeepEqual(schema, want) {
		t.Fatalf("schema %v != fixture want %v", schema, want)
	}
	builder := compositeWorkerWithPolicy(h.AgentRegistry, policy)
	leg, err := builder.BuildWorkerPromptContext(child.ID, child)
	testutil.FailErr(t, "BuildWorkerPromptContext", err)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	renderer := prompts.NewInjectRenderer(engine)
	block, err := inject.RenderWorkerLegInject(context.Background(), renderer, "sess-inject-test", inject.WorkerLegContext(leg))
	testutil.FailErr(t, "RenderWorkerLegInject", err)
	for _, tool := range want {
		if !strings.Contains(block, "- "+tool) {
			t.Fatalf("leg inject missing %q in %q", tool, block)
		}
	}
	if strings.Contains(block, "## Leg tools (allowlisted)\n(none)") {
		t.Fatalf("leg tools block must not show (none): %q", block)
	}
}
