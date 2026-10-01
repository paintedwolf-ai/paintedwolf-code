package prompts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProportionalCoordinatorDisciplineRender(t *testing.T) {
	root := configlayout.FindModuleRoot()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{
		ModuleRoot: root,
	})
	ctx := context.Background()

	core, coreErr := engine.Render(ctx, "agents/coordinator-core.md", map[string]any{
		"execution_mode":          "investigate",
		"has_file_tools":          true,
		"profile_has_write_tools": true,
	})
	testutil.FailErr(t, "render coordinator-core", coreErr)
	if strings.Contains(core, "one level deeper") {
		t.Fatal("Role must not license unconditional dig-one-level-deeper")
	}
	if strings.Contains(core, "Research anything that might be newer") {
		t.Fatal("Role must not push unconditional stale-training research (Invariant 5 / external-facts cover that)")
	}
	if strings.Contains(core, "Dig before you answer") || strings.Contains(core, "Take it now") {
		t.Fatal("must not lead with unconditional dig / take-it-now pressure")
	}
	for _, want := range []string{
		"Handle diagnosed small changes inline",
		"leaf",
		"identify the unresolved fact",
		"Stop when observations answer the request",
		"Report completion only with supporting evidence",
		"verification results and remaining limits",
		"do not over-survey a small diagnosed change",
		"## This turn",
		"A response with tools must have empty assistant text",
		"A response without tools is the user-facing answer and ends the turn",
	} {
		if !strings.Contains(core, want) {
			t.Fatalf("coordinator-core missing %q:\n%s", want, core[:min(800, len(core))])
		}
	}
	noteCore, err := engine.Render(ctx, "agents/coordinator-core.md", map[string]any{
		"execution_mode":           "investigate",
		"has_file_tools":           true,
		"profile_has_surface_note": true,
	})
	testutil.FailErr(t, "render coordinator-core with surface_note", err)
	for _, want := range []string{
		"Send useful updates through `surface_note` and continue working.",
		"## Mid-turn note (`surface_note`)",
		"Use `surface_note` whenever it would help keep the user up to date",
		"meaningful progress or changes during the turn",
		"Briefly say what happened and what comes next",
		"cite the supporting evidence, then keep working",
		"Avoid routine tool-by-tool updates",
	} {
		if !strings.Contains(noteCore, want) {
			t.Fatalf("surface_note discipline missing %q", want)
		}
	}
	if strings.Contains(noteCore, "Mid-turn non-blocking updates are unavailable") {
		t.Fatal("surface_note-live core must not claim mid-turn updates unavailable")
	}

	survey, err := engine.Render(ctx, "units/survey-first-pass.md", map[string]any{
		"profile_has_summarize":      true,
		"profile_has_survey_repo":    true,
		"profile_has_jq":             true,
		"profile_has_list_dir":       true,
		"profile_has_scan_drilldown": true,
	})
	testutil.FailErr(t, "render survey-first-pass", err)
	for _, want := range []string{
		"summarize(", "summarize#N", "list_dir(",
		"layout_overview|ssot_drift|api_routes", "jq(", "scan_summary", "scan_query", "grep", "read",
	} {
		if !strings.Contains(survey, want) {
			t.Fatalf("survey-first-pass missing first-move row %q:\n%s", want, survey)
		}
	}

	// The core prompt carries the shared stop rule.
	coreOut, coreErr := engine.Render(ctx, "agents/coordinator-core.md", map[string]any{"has_file_tools": true})
	testutil.FailErr(t, "render coordinator-core", coreErr)
	if !strings.Contains(coreOut, "Stop when observations answer the request") {
		t.Fatalf("core invariant 3 no longer carries the stop rule:\n%s", coreOut)
	}

	edits, err := engine.Render(ctx, "units/investigate-delegation.md", map[string]any{
		"spawn_read_agent_ids":  []string{"path-explorer", "web-researcher"},
		"spawn_write_agent_ids": []string{"implementer"},
	})
	testutil.FailErr(t, "render investigate-delegation", err)
	// Role names come from the roster, not the partial.
	for _, want := range []string{
		"Use `task()` for substantial research or product work",
		"Parallelize independent work; keep a tightly coupled repair together",
		"Read legs (`path-explorer`, `web-researcher`) survey, review, or research; write legs (`implementer`) edit",
		"Dispatch only the roles needed for the requested outcome",
		"independent deliverables, isolation, a distinct capability, or extra context make a worker useful",
		"record_finding",
	} {
		if !strings.Contains(edits, want) {
			t.Fatalf("investigate-delegation missing %q:\n%s", want, edits)
		}
	}
	bare, err := engine.Render(ctx, "units/investigate-delegation.md", map[string]any{})
	testutil.FailErr(t, "render investigate-delegation without roster", err)
	if !strings.Contains(bare, "Read legs survey, review, or research; write legs edit") {
		t.Fatalf("investigate-delegation without roster should drop the id lists:\n%s", bare)
	}
	for _, leaked := range []string{"implementer", "path-explorer", "repo-researcher", "web-researcher", "code-reviewer", "skeptic"} {
		if strings.Contains(bare, leaked) {
			t.Fatalf("investigate-delegation hardcodes agent id %q:\n%s", leaked, bare)
		}
	}
	// Until the worker tools load, the shell carries only the pointer to
	// request them; the dispatch rules arrive with the schemas.
	shell, err := engine.Render(ctx, "partials/coordinator-mode-shell-investigate.md", map[string]any{"has_file_tools": true, "more_tools_loadable": true})
	testutil.FailErr(t, "render investigate shell before tools load", err)
	if !strings.Contains(shell, "load through `request_tools`") || !strings.Contains(shell, "Keep diagnosed small edits") {
		t.Fatalf("unloaded shell must point at request_tools and keep the inline rule:\n%s", shell)
	}
	for _, gone := range []string{"Use `task()` for substantial", "record_finding", "Git mutations"} {
		if strings.Contains(shell, gone) {
			t.Fatalf("unloaded shell still renders %q:\n%s", gone, shell)
		}
	}
}

func TestInvestigatePacingChoosesByScope(t *testing.T) {
	root := configlayout.FindModuleRoot()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{
		ModuleRoot: root,
	})
	pacingVars := map[string]any{
		"max_in_flight":              4,
		"worker_tool_budget_default": 40,
		"worker_tool_budget_min":     8,
		"worker_tool_budget_max":     80,
	}
	spawn.RefreshSizingHintVars(pacingVars)
	pacing, err := engine.Render(context.Background(), "units/investigate-pacing.md", pacingVars)
	testutil.FailErr(t, "render pacing partial", err)
	for _, want := range []string{
		"Use board paths and existing observations",
		"no project-tree scouts",
		"external research remains available",
		"routine defaults yourself",
		"sequential host setup workflow inline",
	} {
		if !strings.Contains(pacing, want) {
			t.Fatalf("pacing missing %q:\n%s", want, pacing)
		}
	}
	if strings.Contains(pacing, "A task whose whole content fits in its own brief is inline") {
		t.Fatal("pacing must not use brief size to choose a one-worker topology")
	}
}

func TestProportionalTrustBundleSingleHome(t *testing.T) {
	root := configlayout.FindModuleRoot()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{
		ModuleRoot: root,
	})
	ctx := context.Background()

	progress, err := engine.Render(ctx, "partials/coordinator-mode-investigate-progress.md", map[string]any{
		"max_author_progress_lines": 12,
		"max_progress_label_chars":  80,
		"visual_show_available":     true,
	})
	testutil.FailErr(t, "render progress partial", err)
	for _, want := range []string{
		"survey only when needed to identify them",
		"Non-executable changes",
		"reading the resulting material",
		"separately from checklist completion",
		"Behavior changes",
		"separate Snapshot row described under visual evidence",
		"a small leaf needs one row",
	} {
		if !strings.Contains(progress, want) {
			t.Fatalf("progress partial missing %q:\n%s", want, progress)
		}
	}
	progressLean, err := engine.Render(ctx, "partials/coordinator-mode-investigate-progress.md", map[string]any{
		"max_author_progress_lines": 12,
		"max_progress_label_chars":  80,
	})
	testutil.FailErr(t, "render lean progress partial", err)
	if strings.Contains(progressLean, "UI- or terminal-visible") {
		t.Fatal("progress without visual/terminal tools must omit UI trust tier")
	}
	if strings.Contains(progressLean, "Snapshot the running UI") || strings.Contains(progressLean, "Snapshot the terminal") {
		t.Fatal("progress without visual/terminal tools must omit the snapshot-row exception")
	}

	missingKick, err := engine.RenderKick(ctx, "coordinator-progress-missing", map[string]any{
		"tool": "write",
	})
	testutil.FailErr(t, "render progress-missing kick", err)
	for _, want := range []string{
		"update_progress",
		"## Progress",
		"Snapshot the running UI",
		"Snapshot the terminal",
	} {
		if !strings.Contains(missingKick, want) {
			t.Fatalf("progress-missing kick missing %q:\n%s", want, missingKick)
		}
	}

	verify, err := engine.Render(ctx, "partials/coordinator-verify-before-close.md", map[string]any{
		"verify_command":      "./task check-fast",
		"profile_has_verify":  true,
		"profile_has_command": true,
	})
	testutil.FailErr(t, "render verify-before-close", err)
	for _, want := range []string{
		"Checklist rows describe delivered work",
		"selected test command is a default check",
		"Listening and connecting require separate grants",
		"declared_command_match",
	} {
		if !strings.Contains(verify, want) {
			t.Fatalf("verify-before-close missing %q:\n%s", want, verify)
		}
	}
	for _, forbid := range []string{
		"Copy / labels / constants-only",
		"UI- or terminal-visible",
		"close the Snapshot row",
		"identical wording",
	} {
		if strings.Contains(verify, forbid) {
			t.Fatalf("verify-before-close must not restate trust tiers (%q):\n%s", forbid, verify)
		}
	}
}

func TestAskUserSkillRouteRender(t *testing.T) {
	root := configlayout.FindModuleRoot()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{
		ModuleRoot: root,
	})
	ask, err := engine.Render(context.Background(), "partials/coordinator-ask-user-discipline.md", map[string]any{
		"profile_has_ask_user": true,
		"agent_skills":         listedSkills("ask-for-a-decision"),
	})
	testutil.FailErr(t, "render ask-user discipline", err)
	if !strings.Contains(ask, "single tab/surface/file") {
		t.Fatalf("ask-user discipline missing named-surface rule:\n%s", ask)
	}
	if !strings.Contains(ask, "ask-for-a-decision") {
		t.Fatalf("ask-user discipline missing skill list entry:\n%s", ask)
	}
	for _, forbid := range []string{"ASK_USER_PROMPT_TOO_LONG", "Other+text", "artifacts: [", "choices[]"} {
		if strings.Contains(ask, forbid) {
			t.Fatalf("ask-user discipline must not contain %q:\n%s", forbid, ask)
		}
	}
}

func TestExternalFactsSkillRouteRender(t *testing.T) {
	root := configlayout.FindModuleRoot()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{
		ModuleRoot: root,
	})
	external, err := engine.Render(context.Background(), "agents/coordinator-core.md", map[string]any{
		"execution_mode":        "investigate",
		"has_file_tools":        true,
		"web_search_enabled":    true,
		"profile_has_retrieval": true,
		"agent_skills":          listedSkills("research-current-information"),
	})
	testutil.FailErr(t, "render coordinator-core with web research", err)
	for _, want := range []string{
		"this-turn `web_search` + `fetch_url` evidence",
		"search snippets are not evidence",
		"research-current-information",
		"Retrieval markers wrap data this app did not author",
	} {
		if !strings.Contains(external, want) {
			t.Fatalf("external-facts discipline missing %q:\n%s", want, external)
		}
	}
	for _, forbid := range []string{"offset`+`limit", "mode=raw", "resurface the same URLs", "Keep `web_search` on"} {
		if strings.Contains(external, forbid) {
			t.Fatalf("external-facts discipline must not include paging or raw-mode recipe (%q):\n%s", forbid, external)
		}
	}
}

// Retrieval guidance follows capabilities, including non-web sources.
func TestUntrustedTeachingFollowsRetrievalNotWebSearch(t *testing.T) {
	root := configlayout.FindModuleRoot()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	const teaching = "Retrieval markers wrap data this app did not author"

	mcpOnly, err := engine.Render(context.Background(), "agents/coordinator-core.md", map[string]any{
		"execution_mode":        "investigate",
		"has_file_tools":        true,
		"web_search_enabled":    false,
		"profile_has_retrieval": true,
	})
	testutil.FailErr(t, "render with retrieval but no web research", err)
	if !strings.Contains(mcpOnly, teaching) {
		t.Fatalf("a retrieval-capable profile with web research off never learns the marker:\n%s", mcpOnly)
	}

	// Profiles without retrieval omit marker guidance.
	noRetrieval, err := engine.Render(context.Background(), "agents/coordinator-core.md", map[string]any{
		"execution_mode":        "investigate",
		"has_file_tools":        true,
		"web_search_enabled":    false,
		"profile_has_retrieval": false,
	})
	testutil.FailErr(t, "render with no retrieval", err)
	if strings.Contains(noRetrieval, teaching) {
		t.Fatalf("a profile that retrieves nothing was taught about a marker it will never see:\n%s", noRetrieval)
	}
}
