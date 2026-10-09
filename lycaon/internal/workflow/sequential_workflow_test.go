package workflow

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestStartRejectsUntilExit(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	first, err := mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)
	if _, err := mgr.Ambient.StartAmbient(ctx, sessionID, "plan", "1.0.0"); !errors.Is(err, runstate.ErrActiveRunExists) {
		t.Fatalf("err = %v", err)
	}
	if _, err := mgr.Controls.Exit(ctx, sessionID, first.ID, first.Revision, "user_exit"); err != nil {
		testutil.FailErr(t, "mgr.Exit failed", err)
	}
	second, err := startRun(ctx, mgr, sessionID, "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	if second.ID == first.ID {
		t.Fatal("expected new run after exit")
	}
}

func TestMessageStampDuringRun(t *testing.T) {
	mgr, store, _, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	msg := api.Message{Role: api.MessageRoleUser, Content: "hello"}
	if err := mgr.Transcript.StampAndAppendMessages(ctx, "sess-1", msg); err != nil {
		testutil.FailErr(t, "mgr.Transcript.StampAndAppendMessages failed", err)
	}
	msgs, err := store.GetMessages(ctx, "sess-1")
	testutil.FailErr(t, "store.GetMessages failed", err)
	var stamped bool
	for _, m := range msgs {
		if m.Content == "hello" && m.WorkflowRunID == run.ID {
			stamped = true
		}
	}
	if !stamped {
		t.Fatal("expected workflow_run_id on prompt message")
	}
}

func TestStartBoundaryMessageKind(t *testing.T) {
	mgr, store, _, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	msgs, err := store.GetMessages(ctx, "sess-1")
	testutil.FailErr(t, "store.GetMessages failed", err)
	var boundary bool
	for _, m := range msgs {
		if m.Kind == api.MessageKindWorkflowBoundary && m.WorkflowBoundary != nil && m.WorkflowBoundary.Event == "started" {
			boundary = true
			if m.WorkflowRunID != run.ID {
				t.Fatalf("boundary run id = %q", m.WorkflowRunID)
			}
		}
	}
	if !boundary {
		t.Fatal("expected started workflow_boundary message")
	}
}

func TestExitEndBoundaryAndPostureRestore(t *testing.T) {
	mgr, store, _, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	_ = store.UpdateSession(ctx, "sess-1", func(s *api.Session) {
		s.Posture = api.SessionPostureBuild
	})
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	if err != nil {
		testutil.FailErr(t, "startRun failed", err)
	}
	sess, err := store.Get(ctx, "sess-1")
	testutil.FailErr(t, "store.Get failed", err)
	if sess.Posture != api.SessionPostureSpec {
		t.Fatalf("posture during run = %q want spec", sess.Posture)
	}
	exited, err := mgr.Controls.Exit(ctx, "sess-1", run.ID, run.Revision, "user_exit")
	if err != nil {
		testutil.FailErr(t, "mgr.Exit failed", err)
	}
	if exited.PauseReason != "user_exit" {
		t.Fatalf("exit reason = %q want user_exit", exited.PauseReason)
	}
	sess, err = store.Get(ctx, "sess-1")
	testutil.FailErr(t, "store.Get failed", err)
	if sess.Posture != api.SessionPostureBuild {
		t.Fatalf("posture after exit = %q want baseline build", sess.Posture)
	}
	msgs, err := store.GetMessages(ctx, "sess-1")
	testutil.FailErr(t, "store.GetMessages failed", err)
	var sawExitBoundary bool
	for _, m := range msgs {
		if m.Kind == api.MessageKindWorkflowBoundary && m.WorkflowBoundary != nil && m.WorkflowBoundary.Event == "exited" {
			sawExitBoundary = true
		}
	}
	if !sawExitBoundary {
		t.Fatal("expected exited workflow_boundary message")
	}
}
