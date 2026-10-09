package wiring

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// TestCoordinatorPromptDietE2E asserts investigate system prompts from live
// SessionMgr.Prompt assembly (not a render helper). SSOT: docs/coordination.md.
func TestCoordinatorPromptDietE2E(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern: "TODO comment",
			ToolCalls: []llm.MockToolCall{{
				ID:   "t1",
				Name: "task",
				Args: TaskToolArgs("implementer", "Add a // TODO: review here comment at the top of main.go"),
			}},
			FollowUpText: "Queued implementer.",
		},
		{
			Pattern: "Add a // TODO: review here",
			ToolCalls: []llm.MockToolCall{{
				ID:   "w1",
				Name: "write",
				Args: map[string]any{
					"path":    "main.go",
					"content": "// TODO: review here\npackage main\n\nfunc main() {}\n",
				},
			}},
			FollowUpText: MockWorkerCompletionJSON("complete", "Added TODO comment.",
				[]string{"Added the TODO comment to main.go."}),
		},
	}})
	rec := llm.NewRecordingClient(mock)
	h := BuildForTest(t, WithLLMClient(rec))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)

	ctx := context.Background()
	dir := t.TempDir()
	writeTestProjectApprovalsAllowWrite(t, dir)
	_ = writeImplementFixture(t, dir)

	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)
	h.SeedProgress(t, ctx, sess.ID)

	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, "Add a TODO comment via implementer"); err != nil {
		testutil.FailErr(t, "Prompt dispatch", err)
	}
	if err := DrainPendingWorkerJobs(ctx, h, sess.ProjectID, sess.ID); err != nil {
		testutil.FailErr(t, "DrainPendingWorkerJobs", err)
	}

	// Debug.Surface is set only on coordinator turns (stream.go); workers leave it empty.
	investigate := rec.RequestsWhere(func(req modelcall.CompletionRequest) bool {
		return req.Debug.Surface == tools.SurfaceImplementInvestigate
	})
	if len(investigate) == 0 {
		t.Fatal("no investigate-surface coordinator turn recorded")
	}

	forbidden := map[string]string{
		"After worker or debate legs":                      "coordinator-mode-running.md orchestrate block",
		"The host records worker lifecycle in the worklog": "coordination-loop.md worklog copy",
		"which wakes any wait on workers":                  "orchestrate-only worker-budget copy (coordinator-core)",
		"COORDINATOR_ORCHESTRATE_WRITE_DENIED":             "preemptive orchestrate-only error code",
		"state-derived":                                    "mode-derivation theory",
		"EDIT_OLD_STRING_NOT_FOUND":                        "reactive edit-recovery prose (belongs in its hint)",
		"MUTATION_BROKE_PARSE":                             "reactive edit-recovery prose (belongs in its hint)",
		"CODE_REWRITE_PATTERN_TOO_BROAD":                   "reactive rewrite-recovery prose (belongs in its hint)",
	}
	for i, req := range investigate {
		sp := req.SystemPrompt()
		for marker, why := range forbidden {
			if strings.Contains(sp, marker) {
				t.Errorf("investigate turn %d carried orchestrate/recovery copy %q — %s", i, marker, why)
			}
		}
		if !strings.Contains(sp, "Author before mutate/dispatch") {
			t.Errorf("investigate turn %d dropped the progress-authoring guidance", i)
		}
		for _, want := range []string{"summarize(path=…, task=…)", `list_dir({"path":"."})`} {
			if !strings.Contains(sp, want) {
				t.Errorf("investigate turn %d missing always-on survey route %q", i, want)
			}
		}
	}
}
