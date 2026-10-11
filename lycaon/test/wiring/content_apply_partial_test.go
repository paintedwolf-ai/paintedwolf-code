package wiring

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestContentApplyPartialReturnsOneHostComposedResult(t *testing.T) {
	h := BuildForTest(t)
	ctx := h.OwnerCtx(t, context.Background())
	projectDir := t.TempDir()
	review := mustReviewStore(t, h.ConfigRoot)
	testutil.FailErr(t, "enable review policy", review.PutProject(projectDir, settings.ReviewConfig{
		ReviewPaths: []settings.ContentReviewRule{{Path: "**"}},
	}))
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, projectDir)
	testutil.FailErr(t, "create test session", err)
	gate := &toolhost.ContentApplyService{Mgr: h.Sessions.Checkpoints, Review: review}
	before := "one\nkeep a\nkeep b\nthree\n"
	type gateResult struct {
		content string
		err     error
	}
	done := make(chan gateResult, 1)
	go func() {
		content, gateErr := gate.GateApply(ctx, "edit", "notes.txt", &before, "ONE\nkeep a\nkeep b\nTHREE\n", tools.ToolContext{
			Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: projectDir, IsPrimary: true}},
				ActiveRootID: "r1"},
			Identity: tools.InvocationIdentity{SessionID: sess.ID,
				ProjectID: testdbseed.DefaultProjectID},
		})
		done <- gateResult{content: content, err: gateErr}
	}()

	var checkpointID, selectedHunk string
	testutil.WaitFor(t, 3*time.Second, func() bool {
		pending, listErr := h.Sessions.Checkpoints.ListPending(ctx, sess.ID, ptrKind(wire.CheckpointKindContentApply))
		if listErr != nil || len(pending) != 1 || pending[0].ContentApply == nil || len(pending[0].ContentApply.Hunks) != 2 {
			return false
		}
		checkpointID = pending[0].ID
		selectedHunk = pending[0].ContentApply.Hunks[1].ID
		return true
	})
	_, err = h.Sessions.Checkpoints.ResolveCheckpoint(ctx, sess.ID, checkpointID, wire.CheckpointKindContentApply, nil, &hitl.ContentApplyResolve{
		Decision: wire.ContentApplyApprovePartial, ApprovedHunks: []string{selectedHunk},
	})
	testutil.FailErr(t, "approve selected host hunk", err)
	result := <-done
	testutil.FailErr(t, "content_apply gate", result.err)
	if want := "one\nkeep a\nkeep b\nTHREE\n"; result.content != want {
		t.Fatalf("composed content = %q, want %q", result.content, want)
	}
}
