package guidance

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
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
		if _, err := RenderCatalog(context.Background(), ref, fixture); err == nil {
			t.Fatalf("RenderCatalog accepted zero-tool utility template %q without RenderUtilityPrompt", ref)
		}
	}

	for _, ref := range []string{
		SurveyGrepSpecificsRef,
		SurveyFindSpecificsRef,
		SurveyReadSpecificsRef,
		SurveyJQSpecificsRef,
		SurveyJQZoomRef,
		SurveyGrepEmptyTextRef,
		SurveyGrepEmptyStructuralRef,
		SurveyGrepScopeDenseRef,
	} {
		out, err := RenderCatalog(context.Background(), ref, map[string]any{
			"file_count": 12, "count": 8, "bytes": 4000,
		})
		testutil.FailErr(t, "RenderCatalog "+ref, err)
		if strings.TrimSpace(out) == "" {
			t.Fatalf("empty render for %q", ref)
		}
		if _, err := prompts.RenderUtilityPrompt(context.Background(), ref, nil); err == nil {
			t.Fatalf("RenderUtilityPrompt accepted non-allowlisted ref %q", ref)
		}
	}
}

func TestRenderCommandOutputOmittedPairedWitnesses(t *testing.T) {
	setTestRenderer(t)
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	withCommand, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: "implement",
		Profiles:  profiles,
		RootCount: 1,
		HasGit:    true,
		WebSearch: true,
	})
	testutil.FailErr(t, "build reader with command", err)
	withoutCommand, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		Profiles:  profiles,
		Visible:   []string{"recall"},
		RootCount: 1,
		HasGit:    true,
		WebSearch: true,
	})
	testutil.FailErr(t, "build reader without command", err)

	pos, err := RenderCommandOutputOmitted(t.Context(), slices.Contains(withCommand.Offered(), "command"), "4,182", "184 KB", "@output/prog.stdout", 251, 400, 5000, 200000, true)
	testutil.FailErr(t, "render with command", err)
	if !strings.Contains(pos, "(sed -n '251,400p' @output/prog.stdout)") {
		t.Fatalf("positive witness missing sed command hint: %q", pos)
	}

	neg, err := RenderCommandOutputOmitted(t.Context(), slices.Contains(withoutCommand.Offered(), "command"), "4,182", "184 KB", "@output/prog.stdout", 251, 400, 5000, 200000, true)
	testutil.FailErr(t, "render without command", err)
	if strings.Contains(neg, "sed -n") {
		t.Fatalf("negative witness leaked sed command hint without command tool: %q", neg)
	}
	if !strings.Contains(neg, "full output: @output/prog.stdout") {
		t.Fatalf("negative witness lost saved output path notice: %q", neg)
	}
}
