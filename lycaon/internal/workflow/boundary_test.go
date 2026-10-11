package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNewBoundaryMessageAmbientInternal(t *testing.T) {
	run := &api.WorkflowRun{
		WorkflowID: "implement", WorkflowVersion: "1.0.0",
		AttachPolicy: string(workflowdef.AttachPolicySessionCreate), ID: "run-1",
	}
	msg := runstate.NewBoundaryMessage(run, "started", "boot", "")
	if msg.Role != api.MessageRoleSystem {
		t.Fatalf("role = %q want system", msg.Role)
	}
	if msg.Content != "" {
		t.Fatalf("content = %q want empty", msg.Content)
	}
	if msg.Visibility != api.MessageVisibilityInternal {
		t.Fatalf("visibility = %q want internal", msg.Visibility)
	}
	if msg.WorkflowBoundary == nil || msg.WorkflowBoundary.Event != "started" {
		t.Fatalf("meta = %+v", msg.WorkflowBoundary)
	}
}

func TestStartBoundaryMessagesAmbientLeavesScrollAnchorEmpty(t *testing.T) {
	run := &api.WorkflowRun{
		ID: "run-ambient", SessionID: "session", WorkflowID: "implement",
		WorkflowVersion: "1.0.0", AttachPolicy: string(workflowdef.AttachPolicySessionCreate),
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "boot",
	}
	messages, start := runstate.StartBoundaryMessages(run, "", "11111111-1111-4111-8111-111111111111")
	if anchor := api.SpanScrollAnchor(messages, start); anchor != "" {
		t.Fatalf("scroll anchor = %q want empty", anchor)
	}
}

func TestNewBoundaryMessageCatalogTranscript(t *testing.T) {
	run := &api.WorkflowRun{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
		AttachPolicy: "", ID: "run-plan",
	}
	msg := runstate.NewBoundaryMessage(run, "started", "stub", "")
	if msg.Visibility != api.MessageVisibilityTranscript {
		t.Fatalf("visibility = %q want transcript", msg.Visibility)
	}
	if msg.Content != "" {
		t.Fatalf("content = %q want empty", msg.Content)
	}
}

func TestStoreHydratedImplementBoundaryIsInternal(t *testing.T) {
	// Persisted attach policy keeps ambient boundaries internal.
	mgr, store, _, _ := testManager(t)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	run := &api.WorkflowRun{
		ID: "run-ambient", SessionID: sess.ID, WorkflowID: "implement",
		WorkflowVersion: "1.0.0",
		AttachPolicy:    string(workflowdef.AttachPolicySessionCreate),
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    "boot",
	}
	testutil.FailErr(t, "Store.CreateState", mgr.Store.State.CreateState(ctx, run, "", nil))
	loaded, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Store.Get", err)
	if loaded.AttachPolicy != string(workflowdef.AttachPolicySessionCreate) {
		t.Fatalf("store-hydrated AttachPolicy = %q want %q", loaded.AttachPolicy, workflowdef.AttachPolicySessionCreate)
	}
	msg := runstate.NewBoundaryMessage(loaded, "exited", "boot", "superseded_by_workflow_start")
	if msg.Visibility != api.MessageVisibilityInternal {
		t.Fatalf("visibility = %q want internal (session_create implement@)", msg.Visibility)
	}
	if loaded.AttachPolicy != string(workflowdef.AttachPolicySessionCreate) {
		t.Fatalf("AttachPolicy after append = %q want %q", loaded.AttachPolicy, workflowdef.AttachPolicySessionCreate)
	}
}
