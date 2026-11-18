package workflow

import (
	"encoding/json"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
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
	snapshot, err := store.Snapshot(t.Context(), "sess")
	testutil.FailErr(t, "read seeded run", err)
	if snapshot.RunID != "run" {
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

func TestFanoutPlanReplayRepairsFailedProgressCommit(t *testing.T) {
	database := testdbfixture.Open(t, "fanout-replay")
	testdbseed.InsertSessionWithRoot(t, database, "sess-1", testdbseed.DefaultProjectID, t.TempDir())
	sessions := sessionstore.NewSQL(database)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "fanout-test", Version: "1.0.0", AllowedAgents: []string{"path-explorer"},
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "plan", CompleteWhen: "gates_satisfied", Gates: []string{"fanout_planned"}, Next: "execute"},
			{ID: "execute", CompleteWhen: "gates_satisfied", Gates: []string{"worker_cycle_ready"}},
		},
	})
	registry := workflowdef.NewRegistry(map[string]workflowdef.Manifest{workflowdef.ManifestKey(manifest.ID, manifest.Version): manifest})
	mgr := NewManager(testRunStore(t, database), sessions, registry, nil)
	run, err := startRun(t.Context(), mgr, "sess-1", manifest.ID, manifest.Version)
	testutil.FailErr(t, "start fanout", err)
	mgr.Fanout.Progress = progress.NewSQLStore(database, sessions)
	toolsRegistry := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register fanout", RegisterFanoutPlanTool(toolsRegistry, mgr.Fanout))
	args := map[string]any{"legs": []any{map[string]any{"agent_type": "path-explorer", "subject": "Auth boundary", "prompt": "Inspect authorization"}}}
	tctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "sess-1", Agent: "coordinator", ToolCallID: "fanout-call"}}
	_, err = database.ExecContext(t.Context(), `CREATE TRIGGER refuse_plan BEFORE INSERT ON session_progress BEGIN SELECT RAISE(ABORT,'unavailable'); END`)
	testutil.FailErr(t, "refuse progress", err)
	for i := 0; i < 2; i++ {
		result, err := toolsRegistry.Run(t.Context(), "fanout_plan", args, tctx)
		if err == nil || result != "" {
			t.Fatalf("failed seed attempt %d: result=%q err=%v", i, result, err)
		}
		replay, ok, err := mgr.Fanout.Journal.ReplayOperation(t.Context(), "fanout-call", "fanout_plan", args)
		testutil.FailErr(t, "read committed fanout", err)
		if !ok || replay.ID != run.ID {
			t.Fatal("fanout command was not committed before seeding")
		}
		if mgr.Fanout.Progress.Get(t.Context(), "sess-1") != "" {
			t.Fatal("failed seed retained progress")
		}
	}
	_, err = database.ExecContext(t.Context(), `DROP TRIGGER refuse_plan`)
	testutil.FailErr(t, "allow progress", err)
	for i := 0; i < 2; i++ {
		result, err := toolsRegistry.Run(t.Context(), "fanout_plan", args, tctx)
		testutil.FailErr(t, "replay fanout", err)
		var reply FanoutPlanToolResult
		testutil.FailErr(t, "decode reply", json.Unmarshal([]byte(result), &reply))
		if !reply.OK || reply.Legs != 1 {
			t.Fatalf("reply = %+v", reply)
		}
	}
	rows, err := sessions.GetMessages(t.Context(), "sess-1")
	testutil.FailErr(t, "read transcript", err)
	updates := 0
	for _, row := range rows {
		if row.ProgressUpdate != nil {
			updates++
			if row.WorkflowRunID != run.ID || len(row.ProgressUpdate.Steps) != 1 {
				t.Fatalf("progress row = %+v", row)
			}
		}
	}
	if updates != 1 {
		t.Fatalf("progress rows = %d, want 1", updates)
	}
	args["rationale"] = "Different operation payload"
	if _, err := toolsRegistry.Run(t.Context(), "fanout_plan", args, tctx); err == nil {
		t.Fatal("conflicting replay succeeded")
	}
}
