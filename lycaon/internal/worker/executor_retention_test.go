package worker

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestExecuteRetainsChildSessionForDenTranscript(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()

	var childID string
	runner := &fakePromptRunner{
		spawnChild: func(ctx context.Context, parentID string, req api.SpawnChildRequest) (*api.Session, error) {
			parent, err := store.Get(ctx, parentID)
			if err != nil {
				return nil, err
			}
			child, err := store.CreateChild(ctx, parent, req)
			if err != nil {
				return nil, err
			}
			childID = child.ID
			return child, nil
		},
	}
	binder := &fakeChildSessionBinder{}
	exec := NewLocalWorkerExecutor(runner, binder, runner, runner, runner, runner, runner, runner)
	exec.SetPromptInjects(promptstest.InjectRenderer(t))

	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	_, err = exec.Execute(ctx, api.WorkerTask{
		ID:              "job-1",
		ParentSessionID: parent.ID,
		Prompt:          "do work",
		Brief:           "fixture",
		AgentType:       "implementer",
	}, WorkerRunContext{ProjectDir: "/p"})
	testutil.FailErr(t, "exec.Execute failed", err)
	if childID == "" {
		t.Fatal("child session id not captured")
	}
	if binder.jobID != "job-1" || binder.childID != childID {
		t.Fatalf("child binding = %q/%q", binder.jobID, binder.childID)
	}
	if _, err := store.Get(ctx, childID); err != nil {
		t.Fatalf("child session removed after worker completion: %v", err)
	}
}
