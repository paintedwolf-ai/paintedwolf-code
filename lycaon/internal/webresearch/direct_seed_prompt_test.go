package webresearch

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
)

func TestBuildPickSeedsUserMessageIncludesTaskHintAndWindow(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	budget := seedBudgetForHits(5)
	msg, err := buildPickSeedsUserMessage(context.Background(), "steam machine reviews", CurrentPeriod(), "buy a steam machine for TV", budget)
	if err != nil {
		t.Fatalf("buildPickSeedsUserMessage: %v", err)
	}
	if !strings.Contains(msg, "User request") || !strings.Contains(msg, "steam machine for TV") {
		t.Fatalf("task hint missing: %q", msg)
	}
	if !strings.Contains(msg, "Window: current") {
		t.Fatalf("expected the current-window line: %q", msg)
	}
	if !strings.Contains(msg, "verify up to 5 hits") {
		t.Fatalf("expected hit target in message: %q", msg)
	}
	if !strings.Contains(msg, "newer than your training") {
		t.Fatal("expected post-training extrapolation guidance")
	}
}

func TestBuildPickSeedsUserMessageUsesDeclaredWindow(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	budget := seedBudgetForHits(5)

	withQueryYear, err := buildPickSeedsUserMessage(context.Background(), "Steam Machine 2026 review", CurrentPeriod(), "buy a steam machine", budget)
	if err != nil {
		t.Fatalf("buildPickSeedsUserMessage query year: %v", err)
	}
	if !strings.Contains(withQueryYear, "Window: current") {
		t.Fatalf("a year in the query must not move the window: %q", withQueryYear)
	}

	declared, err := buildPickSeedsUserMessage(context.Background(), "steam machine reviews", mustPeriod(t, "2025"), "", budget)
	if err != nil {
		t.Fatalf("buildPickSeedsUserMessage declared: %v", err)
	}
	if !strings.Contains(declared, "Window: 2025") {
		t.Fatalf("expected the declared window line: %q", declared)
	}
	if !strings.Contains(declared, "fresh to false") {
		t.Fatalf("declared past window must tell the picker to drop freshness: %q", declared)
	}
}
