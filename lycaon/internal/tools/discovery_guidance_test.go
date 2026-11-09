package tools

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDiscoveryGuidanceFollowsAvailableContinuation(t *testing.T) {
	wireFixtureGuidanceRenderer(t)
	entries := make([]turnload.DiscoveryEntry, turnload.DiscoveryPageSize+1)
	for i := range entries {
		entries[i] = turnload.DiscoveryEntry{Name: fmt.Sprintf("skill_%02d", i)}
	}
	first, err := DiscoveryPage(t.Context(), ToolContext{}, "skills", "review code", "", "ranking_unavailable", turnload.RankingDisabled, entries)
	testutil.FailErr(t, "discover first page", err)
	if first.NextNeed == "" || !strings.Contains(first.Instruction, "next_need unchanged") || !strings.Contains(first.Instruction, "same catalog") {
		t.Fatalf("first page guidance: %+v", first)
	}
	need, cursor, err := turnload.ParseDiscoveryNeed(first.NextNeed)
	testutil.FailErr(t, "parse page cursor", err)
	last, err := DiscoveryPage(t.Context(), ToolContext{}, "skills", need, cursor, "catalog", "", entries)
	testutil.FailErr(t, "discover final page", err)
	if last.NextNeed != "" || strings.Contains(last.Instruction, "next_need") || !strings.Contains(last.Instruction, "final catalog page") {
		t.Fatalf("final page guidance: %+v", last)
	}
	empty, err := DiscoveryPage(t.Context(), ToolContext{}, "skills", need, "", "catalog", "", nil)
	testutil.FailErr(t, "discover empty catalog", err)
	if empty.NextNeed != "" || strings.Contains(empty.Instruction, "Load") || !strings.Contains(empty.Instruction, "No entries") {
		t.Fatalf("empty catalog guidance: %+v", empty)
	}
}
