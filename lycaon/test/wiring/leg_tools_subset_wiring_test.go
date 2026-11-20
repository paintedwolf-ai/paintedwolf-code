package wiring

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

var exploreAgents = []string{
	orchestration.ProfilePathExplorer,
	orchestration.ProfileRepoResearcher,
	orchestration.ProfileCodeReviewer,
	orchestration.ProfileSecurityReviewer,
}

func TestExploreLegToolsMatchExploreProfile(t *testing.T) {
	for _, agentType := range exploreAgents {
		t.Run(agentType, func(t *testing.T) {
			rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
				{Pattern: ".", Text: MockWorkerCompletionJSON("complete", "Surveyed.", []string{"survey"})},
			}}))
			h := BuildForTest(t, WithLLMClient(rec))
			ctx := context.Background()
			dir := t.TempDir()
			parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
			testutil.FailErr(t, "create session", err)
			task := api.WorkerTask{ID: "job-" + agentType, ParentSessionID: parent.ID, Prompt: "survey", Brief: "fixture", AgentType: agentType}
			testutil.FailErr(t, "worker defaults", worker.ApplyEnqueueDefaults(
				&task, project.ProjectScope{ProjectID: parent.ProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig(),
			))
			_, err = h.Workers.Queue.Enqueue(ctx, task)
			testutil.FailErr(t, "enqueue worker", err)
			claimed, err := h.Workers.Queue.ClaimNext(ctx, worker.ClaimRequest{
				ProjectID: parent.ProjectID, ExecutionTarget: api.ExecutionTargetLocal, ClaimedBy: "leg-tools-test",
			})
			testutil.FailErr(t, "claim worker", err)
			_, err = WiringWorkerExecutor(h).Execute(ctx, *claimed, worker.RunContextForTask(*claimed, dir))
			testutil.FailErr(t, "worker Execute", err)
			stored, ok := h.Workers.Queue.Get(task.ID)
			if !ok || stored == nil || stored.ChildSessionID == "" {
				t.Fatal("worker child session missing")
			}

			var offered []string
			for _, req := range rec.AllRequests() {
				if req.Debug.SessionID == stored.ChildSessionID {
					offered = SortedToolNamesFromMeta(req.Tools)
					break
				}
			}
			if len(offered) == 0 {
				t.Fatalf("no provider request offered tools to child %s", stored.ChildSessionID)
			}
			child, err := h.Store.Get(ctx, stored.ChildSessionID)
			testutil.FailErr(t, "load child", err)
			leg, err := h.Workers.ContextLoader.BuildWorkerPromptContext(workercontext.WithJob(ctx, task.ID), child.ID, child)
			testutil.FailErr(t, "BuildWorkerPromptContext", err)
			got := append([]string(nil), leg.LegTools...)
			sort.Strings(got)
			if !reflect.DeepEqual(got, offered) {
				var onlyLeg, onlyOffered []string
				for _, name := range got {
					if !LegToolsContains(offered, name) {
						onlyLeg = append(onlyLeg, name)
					}
				}
				for _, name := range offered {
					if !LegToolsContains(got, name) {
						onlyOffered = append(onlyOffered, name)
					}
				}
				t.Fatalf("LegTools and offered tools differ: only in LegTools %v, only offered %v", onlyLeg, onlyOffered)
			}
			for _, want := range []string{"command", "complete_leg"} {
				if !LegToolsContains(got, want) {
					t.Fatalf("%s LegTools missing %q: %v", agentType, want, got)
				}
			}
		})
	}
}
