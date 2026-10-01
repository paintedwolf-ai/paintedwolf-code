package guidance

import (
	"context"
	"strings"
	"testing"

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

func TestRenderCatalogUtilityAndSurvey(t *testing.T) {
	setTestRenderer(t)
	for _, ref := range []string{
		UtilityPickSeedsSystemRef,
		UtilityPickSeedsUserRef,
		UtilityRationaleSystemRef,
		UtilityRationaleUserRef,
		UtilityVerifyDetectSystemRef,
		UtilityCommitMessageSystemRef,
		UtilitySessionTitleSystemRef,
		UtilityProjectNameSystemRef,
		UtilityTodayLineRef,
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
			"query": "q", "task_hint": "", "max_seeds": 3, "max_hosts": 4,
			"weekday": "Saturday", "date": "2026-08-15", "cutoff": "2024-01",
			"file_count": 12, "count": 8, "bytes": 4000,
			"goal": "ship", "tool": "command", "command": "ls",
			"user_intent": "ship the feature", "gated_action": "command ls",
		})
		testutil.FailErr(t, "RenderCatalog "+ref, err)
		if strings.TrimSpace(out) == "" {
			t.Fatalf("empty render for %q", ref)
		}
	}
}
