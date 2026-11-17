package workflow

import (
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFanoutPlanSeedsAnEmptyChecklistOnly(t *testing.T) {
	store := progress.NewMemoryStore()
	plan := runstate.FanoutPlan{Legs: []runstate.FanoutPlanLeg{{ID: "leg-1", Subject: "Auth boundary"}, {ID: "leg-2", Subject: "Parser"}}}
	testutil.FailErr(t, "seed plan", seedFanoutProgress(t.Context(), store, "sess", "run", plan))
	content := store.Get(t.Context(), "sess")
	if _, pending, _ := progress.CloseCounts(content); pending != 2 || !strings.Contains(content, "- [ ] leg-1 Auth boundary") {
		t.Fatalf("seeded checklist = %q", content)
	}
	if store.BoundRunID("sess") != "run" {
		t.Fatal("seeding left the document unbound")
	}
	authored := "# Goal\n\nOwn plan\n\n## Progress\n- [x] done already\n"
	testutil.FailErr(t, "author checklist", store.Set("sess", authored))
	testutil.FailErr(t, "seed plan", seedFanoutProgress(t.Context(), store, "sess", "run", plan))
	if strings.TrimSpace(store.Get(t.Context(), "sess")) != strings.TrimSpace(authored) {
		t.Fatal("an authored checklist was overwritten")
	}
}

func TestFanoutSeedingRollsBackAndRetriesWithItsTranscript(t *testing.T) {
	database := testdbfixture.Open(t, "fanout-progress")
	testdbseed.InsertWorkflowRun(t, database, "run", "session", testdbseed.DefaultProjectID)
	sessions := sessionstore.NewSQL(database)
	plans := progress.NewSQLStore(database, sessions)
	plan := runstate.FanoutPlan{Legs: []runstate.FanoutPlanLeg{{ID: "leg", Subject: "Review"}}}
	_, err := database.ExecContext(t.Context(), `CREATE TRIGGER refuse_plan BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT,'unavailable'); END`)
	testutil.FailErr(t, "refuse transcript", err)
	if err := seedFanoutProgress(t.Context(), plans, "session", "run", plan); err == nil {
		t.Fatal("failed seeding reported success")
	}
	if plans.Get(t.Context(), "session") != "" {
		t.Fatal("failed seeding retained plan")
	}
	_, err = database.ExecContext(t.Context(), `DROP TRIGGER refuse_plan`)
	testutil.FailErr(t, "allow transcript", err)
	for i := 0; i < 2; i++ {
		testutil.FailErr(t, "seed or replay plan", seedFanoutProgress(t.Context(), plans, "session", "run", plan))
	}
	rows, err := sessions.GetMessages(t.Context(), "session")
	testutil.FailErr(t, "read seeded transcript", err)
	if len(rows) != 1 || rows[0].WorkflowRunID != "run" || rows[0].ProgressUpdate == nil || len(rows[0].ProgressUpdate.Steps) != 1 {
		t.Fatalf("seeded rows=%+v", rows)
	}
	testutil.FailErr(t, "ignore another run", seedFanoutProgress(t.Context(), plans, "session", "other", plan))
	rows, err = sessions.GetMessages(t.Context(), "session")
	testutil.FailErr(t, "read unchanged transcript", err)
	if len(rows) != 1 {
		t.Fatal("foreign run seeded progress")
	}
}
