package inject_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRenderTurnStateBlock(t *testing.T) {
	root := testConfigRoot(t)
	renderer := prompts.NewInjectRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root}))

	t.Run("empty inputs render empty", func(t *testing.T) {
		block, err := inject.RenderTurnStateBlock(context.Background(), renderer, "sess-empty", inject.TurnStateInjectInput{})
		testutil.FailErr(t, "RenderTurnStateBlock", err)
		if block != "" {
			t.Fatalf("expected empty block, got: %q", block)
		}
	})

	t.Run("progress missing with update_progress", func(t *testing.T) {
		block, err := inject.RenderTurnStateBlock(context.Background(), renderer, "sess-progress", inject.TurnStateInjectInput{
			ProgressMissing: true,
			ToolVars:        map[string]any{"profile_has_update_progress": true},
		})
		testutil.FailErr(t, "RenderTurnStateBlock", err)
		if !strings.Contains(block, "No checklist is stored") {
			t.Fatalf("expected checklist warning, got: %q", block)
		}
	})

	t.Run("progress missing without update_progress", func(t *testing.T) {
		block, err := inject.RenderTurnStateBlock(context.Background(), renderer, "sess-progress-no-tool", inject.TurnStateInjectInput{
			ProgressMissing: true,
		})
		testutil.FailErr(t, "RenderTurnStateBlock", err)
		if strings.Contains(block, "No checklist is stored") {
			t.Fatalf("unexpected checklist warning without tool, got: %q", block)
		}
	})

	t.Run("verify required", func(t *testing.T) {
		_, err := inject.RenderTurnStateBlock(context.Background(), renderer, "sess-verify", inject.TurnStateInjectInput{
			VerifyRequired: true,
		})
		testutil.FailErr(t, "RenderTurnStateBlock", err)
	})
}

func TestTurnStateCarriesCurrentProgressSnapshot(t *testing.T) {
	renderer := prompts.NewInjectRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: testConfigRoot(t)}))
	snapshot := `{"content":"- [ ] Deliver","revision":"current-snapshot-revision","reconciliation":{"token":"worker-finish-token","jobs":["worker-one"]}}`
	block, err := inject.RenderTurnStateBlock(t.Context(), renderer, "snapshot", inject.TurnStateInjectInput{ProgressSnapshot: snapshot, ToolVars: map[string]any{"profile_has_update_progress": true}})
	testutil.FailErr(t, "render current progress", err)
	for _, value := range []string{"current-snapshot-revision", "worker-finish-token", "worker-one", "Deliver"} {
		if !strings.Contains(block, value) {
			t.Fatalf("snapshot omitted %q: %s", value, block)
		}
	}
}

// Every roster state renders its row and the values the host supplied, and
// names a tool only when the request offers it.
func TestTurnStateRosterRendersEveryState(t *testing.T) {
	renderer := prompts.NewInjectRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: testConfigRoot(t)}))
	stateTools := map[string][]string{
		"asking":     {"answer_decision"},
		"delivered":  {"preview_overlay", "promote_overlay", "reject_overlay", "shelve_overlay", "instruct_worker"},
		"failed":     {"instruct_worker"},
		"idle":       {"instruct_worker"},
		"working":    nil,
		"waiting":    nil,
		"planned":    nil,
		"recovering": nil,
	}
	allTools := map[string]any{}
	for _, tools := range stateTools {
		for _, tool := range tools {
			allTools["profile_has_"+tool] = true
		}
	}
	for state, tools := range stateTools {
		row := map[string]any{
			"name": "w-" + state, "agent": "implementer", "scope_mode": "write", "state": state,
			"about": "about-" + state, "question": "question-" + state,
			"result_handle": "handle-" + state, "result_disposition": "open",
			"changed": []string{"changed/" + state}, "read": []string{"read/" + state},
		}
		for _, offered := range []bool{true, false} {
			in := inject.TurnStateInjectInput{Workers: []map[string]any{row}}
			if offered {
				in.ToolVars = allTools
			}
			block, err := inject.RenderTurnStateBlock(t.Context(), renderer, "roster-"+state, in)
			testutil.FailErr(t, "render roster "+state, err)
			for _, value := range []string{"w-" + state, "about-" + state, "question-" + state, "handle-" + state, "changed/" + state, "read/" + state} {
				if !strings.Contains(block, value) {
					t.Errorf("%s (offered=%v) omitted %q: %s", state, offered, value, block)
				}
			}
			for _, tool := range tools {
				if strings.Contains(block, "`"+tool+"`") != offered {
					t.Errorf("%s (offered=%v) mentions %s wrongly: %s", state, offered, tool, block)
				}
			}
		}
	}
}

func TestTurnStateNamesOmittedWorkers(t *testing.T) {
	renderer := prompts.NewInjectRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: testConfigRoot(t)}))
	block, err := inject.RenderTurnStateBlock(t.Context(), renderer, "roster-omitted", inject.TurnStateInjectInput{
		Workers:        []map[string]any{{"name": "shown", "agent": "explorer", "state": "idle"}},
		WorkersOmitted: 7,
	})
	testutil.FailErr(t, "render omitted roster", err)
	if !strings.Contains(block, "shown") || !strings.Contains(block, "7") {
		t.Fatalf("omitted workers not counted: %s", block)
	}
}
