package workflow

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

func TestFanoutPlanSeedsAnEmptyChecklistOnly(t *testing.T) {
	store := progress.NewMemoryStore()
	plan := runstate.FanoutPlan{Legs: []runstate.FanoutPlanLeg{{ID: "leg-1", Subject: "Auth boundary"}, {ID: "leg-2", Subject: "Parser"}}}
	seedFanoutProgress(t.Context(), store, "sess", "run", plan)
	content := store.Get(t.Context(), "sess")
	if _, pending, _ := progress.CloseCounts(content); pending != 2 || !strings.Contains(content, "- [ ] leg-1 Auth boundary") {
		t.Fatalf("seeded checklist = %q", content)
	}
	if store.BoundRunID("sess") != "run" {
		t.Fatal("seeding left the document unbound")
	}
	authored := "# Goal\n\nOwn plan\n\n## Progress\n- [x] done already\n"
	testutil.FailErr(t, "author checklist", store.Set("sess", authored))
	seedFanoutProgress(t.Context(), store, "sess", "run", plan)
	if strings.TrimSpace(store.Get(t.Context(), "sess")) != strings.TrimSpace(authored) {
		t.Fatal("an authored checklist was overwritten")
	}
}
