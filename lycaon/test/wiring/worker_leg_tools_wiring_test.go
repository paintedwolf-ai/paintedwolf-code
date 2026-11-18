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
	"github.com/lycaon/lycaon/internal/session/workercontext"
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
	builder := h.Workers.ContextLoader

	for _, agentType := range implementDefaultAgents {
		t.Run(agentType, func(t *testing.T) {
			child, err := h.Sessions.Manager.Workers.SpawnChild(ctx, sess.ID, api.SpawnChildRequest{
				AgentType: agentType,
				Prompt:    "survey",
			})
			testutil.FailErr(t, "SpawnChild", err)
			prof, err := h.AgentRegistry.Get(agentType)
			testutil.FailErr(t, "agents.Get", err)
			// skills_read follows the profile's skill catalog, which the explore
			// leg test checks against the offered request; compare the rest.
			var schema []string
			for _, name := range sortedToolNamesFromMeta(policy.ListForPrompt(ctx, child, prof.ToolProfile)) {
				if name != "skills_read" {
					schema = append(schema, name)
				}
			}
			leg, err := builder.BuildWorkerPromptContext(workercontext.WithJob(t.Context(), "leg-tools-"+agentType), child.ID, child)
			testutil.FailErr(t, "BuildWorkerPromptContext", err)
			var got []string
			for _, name := range leg.LegTools {
				if name != "skills_read" {
					got = append(got, name)
				}
			}
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
			if agentType == orchestration.ProfilePathExplorer && !legToolsContains(got, "command") {
				t.Fatalf("path-explorer LegTools missing command: %v", got)
			}
		})
	}
}

// implementerLegToolsWant is the isolated implementation profile.
var implementerLegToolsWant = []string{
	"capture_page", "code_rewrite", "summarize", "survey_repo", "command", "command_output", "command_stop", "complete_leg", "fetch_url", "handoff_release", "handoff_release_all", "handoff_reserve", "held_result", "held_stop", "measure_page", "message_coordinator", "pack_board", "page_act", "page_close", "page_open", "page_snapshot", "read_findings", "read_review", "recall", "record_finding", "render_view", "request_decision", "request_tools", "restore_version", "scan_compare", "scan_list", "scan_pack", "scan_query", "scan_summary", "secret_generate", "secret_list", "secret_revoke", "skills_read", "source_history", "terminal_close", "terminal_open", "terminal_read", "terminal_send", "terminal_snapshot", "verify", "view_image", "view_video", "wait", "web_search",
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
	leg, err := builder.BuildWorkerPromptContext(t.Context(), child.ID, child)
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
	if strings.Contains(block, "## Assignment tools (allowlisted)\n(none)") {
		t.Fatalf("assignment tools block must not show (none): %q", block)
	}
}
