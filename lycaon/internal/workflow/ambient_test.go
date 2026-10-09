package workflow

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestIsSessionAmbientRootMachineState(t *testing.T) {
	mgr, store, _, _ := testManager(t)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	bundledDir := filepath.Join(filepath.Dir(file), "..", "..", "config", "packs", "painted-wolf", "platform", "workflows")
	ref, err := workflowdef.LoadRegistryConfig(extpacks.OnDisk(bundledDir))
	testutil.FailErr(t, "LoadRegistryConfig", err)
	ambient, err := mgr.Ambient.StartAmbient(ctx, sess.ID, ref.ID, ref.Version)
	testutil.FailErr(t, "StartAmbient", err)

	// Persisted attachment policy identifies ambient runs after reload.
	if !runstate.IsAmbientRun(ambient) {
		t.Fatal("fresh ambient start must be session ambient root")
	}
	loaded, err := mgr.Store.Runs.Get(ctx, ambient.ID)
	testutil.FailErr(t, "Get ambient", err)
	if loaded.AttachPolicy != string(workflowdef.AttachPolicySessionCreate) {
		t.Fatalf("store-hydrated AttachPolicy = %q want %q", loaded.AttachPolicy, workflowdef.AttachPolicySessionCreate)
	}
	if !runstate.IsAmbientRun(loaded) {
		t.Fatal("store-hydrated ambient root must use persisted attach.policy + no parent")
	}

	parentID := ambient.ID
	child := &api.WorkflowRun{
		ID:              "child-implement",
		SessionID:       sess.ID,
		WorkflowID:      ref.ID,
		WorkflowVersion: ref.Version,
		AttachPolicy:    string(workflowdef.AttachPolicySessionCreate),
		ParentRunID:     &parentID,
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "boot",
	}
	if runstate.IsAmbientRun(child) {
		t.Fatal("child implement@ must not be session ambient root")
	}

	catalog := &api.WorkflowRun{
		ID: "catalog-plan", SessionID: sess.ID, WorkflowID: "plan",
		WorkflowVersion: "1.0.0", AttachPolicy: "",
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "boot",
	}
	if runstate.IsAmbientRun(catalog) {
		t.Fatal("catalog root must not be session ambient root")
	}
}

func TestExitUsesPersistedAttachmentAfterCatalogChanges(t *testing.T) {
	for _, ambient := range []bool{true, false} {
		name := "catalog"
		if ambient {
			name = "ambient"
		}
		t.Run(name, func(t *testing.T) {
			mgr, _, _, _ := testManagerWithRegistry(t)
			ctx := t.Context()
			var run *api.WorkflowRun
			var err error
			if ambient {
				run, err = mgr.Ambient.StartAmbient(ctx, "sess-1", "implement", "1.0.0")
			} else {
				run, err = startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
			}
			testutil.FailErr(t, "start workflow", err)
			manifest, err := mgr.Resolver.ForRun(ctx, run)
			testutil.FailErr(t, "read original manifest", err)
			manifest.Attach.Policy = workflowdef.AttachPolicySessionCreate
			if ambient {
				manifest.Attach.Policy = ""
			}
			mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{workflowdef.ManifestKey(manifest.ID, manifest.Version): manifest})
			_, err = mgr.Controls.Exit(ctx, run.SessionID, run.ID, run.Revision, "exit reviewed run")
			testutil.FailErr(t, "exit after catalog change", err)
			settled, err := mgr.Store.Runs.Get(ctx, run.ID)
			testutil.FailErr(t, "read exited run", err)
			if settled.Status != api.WorkflowRunStatusCanceled {
				t.Fatalf("exited run status = %s", settled.Status)
			}
			active, err := mgr.Store.Runs.ActiveBySession(ctx, run.SessionID)
			testutil.FailErr(t, "read active workflow", err)
			if ambient && active != nil {
				t.Fatalf("ambient exit respawned workflow: %+v", active)
			}
			if !ambient && (active == nil || !runstate.IsAmbientRun(active)) {
				t.Fatalf("catalog exit must attach a fresh ambient workflow: %+v", active)
			}
		})
	}
}
