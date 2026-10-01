package wiring

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestParallelWorkerRecordFindingSiblingInjectWiring(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()

	parent, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create parent", err)

	overlayA := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-a")
	overlayB := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-b")
	spawnAt := time.Now().UTC()

	taskA := wire.WorkerTask{
		ID:              "job-a",
		ParentSessionID: parent.ID,
		AgentType:       "implementer",
		Prompt:          "leg a",
		Brief:           "fixture",
		Status:          wire.WorkerStatusRunning,
		WorkspaceRoot:   overlayA,
		Scope:           &wire.TaskScope{Mode: wire.TaskScopeModeWrite, Paths: []string{"src/**"}},
		CreatedAt:       spawnAt,
	}
	taskB := wire.WorkerTask{
		ID:              "job-b",
		ParentSessionID: parent.ID,
		AgentType:       "implementer",
		Prompt:          "leg b",
		Brief:           "fixture",
		Status:          wire.WorkerStatusRunning,
		WorkspaceRoot:   overlayB,
		Scope:           &wire.TaskScope{Mode: wire.TaskScopeModeWrite, Paths: []string{"pkg/**"}},
		CreatedAt:       spawnAt,
	}
	testutil.FailErr(t, "defaults a", worker.ApplyEnqueueDefaults(&taskA, project.ProjectScope{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig()))
	testutil.FailErr(t, "defaults b", worker.ApplyEnqueueDefaults(&taskB, project.ProjectScope{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir}, worker.DefaultWorkersConfig()))
	_, err = h.WorkerQueue.Enqueue(ctx, taskA)
	testutil.FailErr(t, "enqueue a", err)
	_, err = h.WorkerQueue.Enqueue(ctx, taskB)
	testutil.FailErr(t, "enqueue b", err)

	childA, err := h.Store.CreateChild(ctx, parent, wire.SpawnChildRequest{AgentType: "implementer", Prompt: "leg a"})
	testutil.FailErr(t, "create child a", err)
	childB, err := h.Store.CreateChild(ctx, parent, wire.SpawnChildRequest{AgentType: "implementer", Prompt: "leg b"})
	testutil.FailErr(t, "create child b", err)
	testutil.FailErr(t, "link a", h.WorkerQueue.SetChildSessionID(ctx, taskA.ID, childA.ID))
	testutil.FailErr(t, "link b", h.WorkerQueue.SetChildSessionID(ctx, taskB.ID, childB.ID))

	configDir := filepath.Join(dir, "internal", "config")
	testutil.FailErr(t, "create config dir", os.MkdirAll(configDir, 0o755))
	testutil.FailErr(t, "write resolver", os.WriteFile(
		filepath.Join(configDir, "resolve.go"), []byte("package config\n"), 0o644,
	))
	testutil.FailErr(t, "store finding evidence", h.Store.UpsertEvidenceRecord(ctx, childA.ID, evidence.Record{
		Handle: "read#1", Kind: "read", Shape: "file_region", Fidelity: "structured",
		SourceTool: "read", Path: "internal/config/resolve.go",
		LineRanges: []evidence.LineRange{{Start: 1, End: 1}}, Body: []string{"package config"},
	}))
	tctx := wiringToolContext(childA.ID, dir, taskA.ID)

	summary := "config resolver lives in internal/config/resolve.go:1"
	_, err = h.ToolRegistry.Run(ctx, "record_finding", map[string]any{
		"summary": summary,
		"ref":     "internal/config/resolve.go:1",
	}, tctx)
	testutil.FailErr(t, "record_finding worker a", err)

	notes, _, err := h.SessionMgr.RecentSiblingNotes(ctx, childB.ID, 0, 5)
	testutil.FailErr(t, "read sibling notes", err)
	if len(notes) != 1 || notes[0].Summary != summary {
		t.Fatalf("sibling notes for B = %+v want summary %q", notes, summary)
	}

	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	renderer := prompts.NewInjectRenderer(engine)
	block, err := inject.RenderWorkerLegInject(ctx, renderer, "sess-inject-test", inject.WorkerLegContext{
		LegID:    "leg-b",
		LegTools: []string{"read"},
		SiblingNotes: []inject.SiblingNote{
			{Agent: notes[0].Agent, Summary: notes[0].Summary, Ref: notes[0].Ref},
		},
	})
	testutil.FailErr(t, "render worker leg", err)
	// Sibling findings retain peer attribution.
	for _, want := range []string{"## Peer findings", "**" + notes[0].Agent + ":**", summary, "(" + notes[0].Ref + ")"} {
		if !strings.Contains(block, want) {
			t.Fatalf("worker leg inject missing %q: %q", want, block)
		}
	}
}
