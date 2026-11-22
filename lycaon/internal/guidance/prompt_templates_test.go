package guidance

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolreach"
)

func TestRenderCompactionSystemPrompts(t *testing.T) {
	setTestRenderer(t)
	for _, ref := range []string{
		CompactionSessionSummarySystemRef,
		CompactionChunkSummarySystemRef,
	} {
		out, err := RenderCompactionSystemPrompt(context.Background(), ref)
		testutil.FailErr(t, "RenderCompactionSystemPrompt "+ref, err)
		if strings.TrimSpace(out) == "" {
			t.Fatalf("empty render for %q", ref)
		}
	}
}

func TestRenderCuratorPrompts(t *testing.T) {
	setTestRenderer(t)
	sys, err := RenderCuratorSystemPrompt(context.Background())
	testutil.FailErr(t, "RenderCuratorSystemPrompt", err)
	if strings.TrimSpace(sys) == "" {
		t.Fatal("empty curator system prompt")
	}
	user, err := RenderCuratorUserPrompt(context.Background(), map[string]any{
		"focus":  "entry",
		"budget": 3,
		"candidates": []map[string]any{
			{"handle": "read#1", "path": "a.go", "lines": "1", "preview": "1| package a"},
		},
	})
	testutil.FailErr(t, "RenderCuratorUserPrompt", err)
	if !strings.Contains(user, "entry") {
		t.Fatalf("user prompt missing focus: %q", user)
	}
}

func TestRenderUtilityPromptsAndSurveySeparation(t *testing.T) {
	setTestRenderer(t)
	fixture := map[string]any{
		"query": "q", "task_hint": "", "max_seeds": 3, "max_hosts": 4,
		"min_leads": 2, "max_leads": 5, "expand_min": 2, "expand_max": 4,
		"hit_target": 5, "period_current": true, "period": "current",
		"weekday": "Saturday", "date": "2026-08-15", "cutoff": "2024-01",
		"goal": "ship", "tool": "command", "command": "ls", "steps": "1. ls",
		"user_intent": "ship the feature", "action": "command ls",
		"result_handle": "h1", "assignment": "fix", "outcome": "complete",
		"current_request": "continue", "prior_continuation": "{}",
		"recent_messages": []map[string]any{{"role": "user", "origin": "user", "authority": "user", "trust_tier": "trusted", "content": "hi"}},
		"focus":           "entry", "budget": 3,
		"candidates": []map[string]any{{"handle": "read#1", "path": "a.go", "lines": "1", "preview": "1| package a"}},
	}
	for _, ref := range prompts.ZeroToolUtilityTemplates() {
		out, err := prompts.RenderUtilityPrompt(context.Background(), ref, fixture)
		testutil.FailErr(t, "RenderUtilityPrompt "+ref, err)
		if strings.TrimSpace(out) == "" {
			t.Fatalf("empty render for %q", ref)
		}
		if _, err := RenderGuidance(context.Background(), ref, fixture); err == nil {
			t.Fatalf("RenderGuidance accepted zero-tool utility template %q without RenderUtilityPrompt", ref)
		}
	}

	for _, ref := range []string{
		SurveyJQZoomRef,
		SurveyGrepEmptyTextRef,
		SurveyGrepEmptyStructuralRef,
		SurveyGrepScopeDenseRef,
	} {
		out, err := RenderGuidance(context.Background(), ref, map[string]any{
			"file_count": 12, "count": 8, "bytes": 4000,
		})
		testutil.FailErr(t, "RenderGuidance "+ref, err)
		if strings.TrimSpace(out) == "" {
			t.Fatalf("empty render for %q", ref)
		}
		if _, err := prompts.RenderUtilityPrompt(context.Background(), ref, nil); err == nil {
			t.Fatalf("RenderUtilityPrompt accepted non-allowlisted ref %q", ref)
		}
	}

	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: "implement",
		Profiles:  profiles,
		RootCount: 1,
		HasGit:    true,
		WebSearch: true,
	})
	testutil.FailErr(t, "build reader", err)
	gr := prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	for _, ref := range []string{
		"survey/grep-specifics",
		"survey/find-specifics",
		"survey/read-specifics",
		"survey/jq-specifics",
	} {
		if _, err := RenderGuidance(context.Background(), ref, nil); err == nil {
			t.Fatalf("RenderGuidance accepted non-structural ref %q without Reader", ref)
		}
		out, err := gr.RenderWithReader(context.Background(), reader, ref, nil)
		testutil.FailErr(t, "RenderWithReader "+ref, err)
		if strings.TrimSpace(out) == "" {
			t.Fatalf("empty render for %q", ref)
		}
	}
}

func TestRenderCommandOutputOmittedPairedWitnesses(t *testing.T) {
	setTestRenderer(t)
	sedAdmitted := toolreach.Offered([]string{toolreach.ProgramTool}).WithPrograms(func(name string) bool {
		return name == "sed"
	})
	sedDenied := toolreach.Offered([]string{toolreach.ProgramTool}).WithPrograms(func(name string) bool {
		return name != "sed"
	})
	withoutCommand := toolreach.Offered([]string{"recall"})

	pos, err := RenderCommandOutputOmitted(t.Context(), sedAdmitted, "4,182", "184 KB", "@output/prog.stdout", 251, 400, 5000, 200000, true)
	testutil.FailErr(t, "render with sed admitted", err)
	if !strings.Contains(pos, "(sed -n '251,400p' @output/prog.stdout)") {
		t.Fatalf("positive witness missing sed command hint: %q", pos)
	}

	quotedPath := "@output/my file's.stdout"
	quotedOut, err := RenderCommandOutputOmitted(t.Context(), sedAdmitted, "4,182", "184 KB", quotedPath, 251, 400, 5000, 200000, true)
	testutil.FailErr(t, "render quoted path", err)
	if !strings.Contains(quotedOut, "full output: @output/my file's.stdout") {
		t.Fatalf("quoted witness lost raw saved path: %q", quotedOut)
	}
	if !strings.Contains(quotedOut, `(sed -n '251,400p' @output/'my file'\''s.stdout')`) {
		t.Fatalf("quoted witness did not safely quote command operand: %q", quotedOut)
	}

	denied, err := RenderCommandOutputOmitted(t.Context(), sedDenied, "4,182", "184 KB", quotedPath, 251, 400, 5000, 200000, true)
	testutil.FailErr(t, "render with sed denied", err)
	if strings.Contains(denied, "sed -n") {
		t.Fatalf("sed-denied witness leaked sed command hint: %q", denied)
	}
	if !strings.Contains(denied, "full output: @output/my file's.stdout") {
		t.Fatalf("sed-denied witness lost saved output path notice: %q", denied)
	}

	neg, err := RenderCommandOutputOmitted(t.Context(), withoutCommand, "4,182", "184 KB", "@output/prog.stdout", 251, 400, 5000, 200000, true)
	testutil.FailErr(t, "render without command", err)
	if strings.Contains(neg, "sed -n") {
		t.Fatalf("negative witness leaked sed command hint without command tool: %q", neg)
	}
	if !strings.Contains(neg, "full output: @output/prog.stdout") {
		t.Fatalf("negative witness lost saved output path notice: %q", neg)
	}
}
