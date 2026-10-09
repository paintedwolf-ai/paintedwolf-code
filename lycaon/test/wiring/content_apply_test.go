package wiring

import (
	"context"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestContentApplyDefaultOffParity(t *testing.T) {
	h := BuildForTest(t)
	review := mustReviewStore(t, h.ConfigRoot)
	ctx := context.Background()
	projectDir := t.TempDir()
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, projectDir)
	testutil.FailErr(t, "create test session", err)
	gate := &toolhost.ContentApplyService{
		Mgr:    h.CheckpointMgr,
		Review: review,
	}
	after, err := gate.GateApply(ctx, "write", "a.txt", nil, "hello", tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: projectDir, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{SessionID: sess.ID,
			ProjectID: testdbseed.DefaultProjectID},
	})
	if err != nil {
		testutil.FailErr(t, "content_apply gate should passthrough when review policy off", err)
	}
	if after != "hello" {
		t.Fatalf("after = %q want passthrough", after)
	}
}

func TestContentApplyEmittedWhenPolicyOn(t *testing.T) {
	h := BuildForTest(t)
	ctx := h.OwnerCtx(t, context.Background())
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, settingsoverlay.DirName()), 0o755); err != nil {
		testutil.FailErr(t, "create project overlay directory", err)
	}
	reviewBody := "review_paths:\n  - path: '**'\n"
	if err := os.WriteFile(filepath.Join(projectDir, settingsoverlay.DirName(), "review.yaml"), []byte(reviewBody), 0o600); err != nil {
		testutil.FailErr(t, "write project review.yaml with a review_paths rule", err)
	}
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, projectDir)
	testutil.FailErr(t, "create test session", err)
	gate := &toolhost.ContentApplyService{
		Mgr:    h.CheckpointMgr,
		Review: mustReviewStore(t, h.ConfigRoot),
	}
	done := make(chan error, 1)
	go func() {
		_, err := gate.GateApply(ctx, "write", "review.txt", nil, "pending bytes", tools.ToolContext{
			Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: projectDir, IsPrimary: true}},
				ActiveRootID: "r1"},
			Identity: tools.InvocationIdentity{SessionID: sess.ID,
				ProjectID: testdbseed.DefaultProjectID},
		})
		done <- err
	}()
	var checkpointID string
	testutil.WaitFor(t, 3*time.Second, func() bool {
		pending, err := h.CheckpointMgr.ListPending(ctx, sess.ID, ptrKind(wire.CheckpointKindContentApply))
		if err == nil && len(pending) == 1 {
			checkpointID = pending[0].ID
			return true
		}
		return false
	})
	if _, err := h.CheckpointMgr.ResolveCheckpoint(ctx, sess.ID, checkpointID, wire.CheckpointKindContentApply, nil, &hitl.ContentApplyResolve{
		Decision: wire.ContentApplyApprove,
	}); err != nil {
		testutil.FailErr(t, "approve content_apply checkpoint", err)
	}
	if err := <-done; err != nil {
		testutil.FailErr(t, "content_apply gate should complete after checkpoint approval", err)
	}
}

func TestContentApplyAppliesWhenPathNotReviewed(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, settingsoverlay.DirName()), 0o755); err != nil {
		testutil.FailErr(t, "create project overlay directory", err)
	}
	// Review is targeted: only src/** is reviewed, so a write to docs/ applies directly.
	reviewBody := "review_paths:\n  - path: 'src/**'\n"
	if err := os.WriteFile(filepath.Join(projectDir, settingsoverlay.DirName(), "review.yaml"), []byte(reviewBody), 0o600); err != nil {
		testutil.FailErr(t, "write project review.yaml with a review_paths rule", err)
	}
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, projectDir)
	testutil.FailErr(t, "create test session", err)
	gate := &toolhost.ContentApplyService{
		Mgr:    h.CheckpointMgr,
		Review: mustReviewStore(t, h.ConfigRoot),
	}
	after, err := gate.GateApply(ctx, "write", "docs/readme.md", nil, "instant", tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: projectDir, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{SessionID: sess.ID,
			ProjectID: testdbseed.DefaultProjectID},
	})
	if err != nil {
		testutil.FailErr(t, "content_apply gate should pass through a path outside review_paths", err)
	}
	if after != "instant" {
		t.Fatalf("after = %q want instant passthrough", after)
	}
	pending, err := h.CheckpointMgr.ListPending(ctx, sess.ID, nil)
	testutil.FailErr(t, "list pending checkpoints", err)
	if len(pending) != 0 {
		t.Fatalf("expected no checkpoint for an unreviewed path, got %d pending: %+v", len(pending), pending)
	}
}

func TestContentApplyRejectStructuredError(t *testing.T) {
	h := BuildForTest(t)
	review := mustReviewStore(t, h.ConfigRoot)
	ctx := h.OwnerCtx(t, context.Background())
	projectDir := t.TempDir()
	if err := review.PutProject(projectDir, settings.ReviewConfig{ReviewPaths: []settings.ContentReviewRule{{Path: "**"}}}); err != nil {
		testutil.FailErr(t, "enable review policy on project", err)
	}
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, projectDir)
	testutil.FailErr(t, "create test session", err)
	gate := &toolhost.ContentApplyService{Mgr: h.CheckpointMgr, Review: review}
	done := make(chan error, 1)
	go func() {
		_, err := gate.GateApply(ctx, "write", "x.txt", nil, "nope", tools.ToolContext{
			Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: projectDir, IsPrimary: true}},
				ActiveRootID: "r1"},
			Identity: tools.InvocationIdentity{SessionID: sess.ID,
				ProjectID: testdbseed.DefaultProjectID},
		})
		done <- err
	}()
	var checkpointID string
	testutil.WaitFor(t, 3*time.Second, func() bool {
		pending, err := h.CheckpointMgr.ListPending(ctx, sess.ID, ptrKind(wire.CheckpointKindContentApply))
		if err == nil && len(pending) == 1 {
			checkpointID = pending[0].ID
			return true
		}
		return false
	})
	if _, err := h.CheckpointMgr.ResolveCheckpoint(ctx, sess.ID, checkpointID, wire.CheckpointKindContentApply, nil, &hitl.ContentApplyResolve{
		Decision: wire.ContentApplyReject,
		Guidance: "shorten the timeout instead",
	}); err != nil {
		testutil.FailErr(t, "reject content_apply checkpoint", err)
	}
	gateErr := <-done
	reject := toolrejection.AsToolReject(gateErr)
	if reject == nil {
		t.Fatalf("err = %v want structured ToolReject", gateErr)
	}
	if reject.Code != "CONTENT_APPLY_REJECTED" {
		t.Fatalf("code = %q want CONTENT_APPLY_REJECTED", reject.Code)
	}
	if got := reject.Data["user_guidance"]; got != "shorten the timeout instead" {
		t.Fatalf("user_guidance = %v want composer direction", got)
	}
}

// Content review must hold every tool on the catalog's mutates_content axis, so
// the set is read from the catalog rather than listed here. Host-produced
// identities are skipped: content_apply is the checkpoint this gate raises, not
// a tool passed into it.
func TestContentApplyHoldsEveryContentMutatingTool(t *testing.T) {
	cfg, err := nativemanifest.Load()
	testutil.FailErr(t, "load native tool manifest", err)

	for _, tool := range cfg.ContentMutatingTools() {
		if cfg.HasHostProducedTool(tool) {
			continue
		}
		t.Run(tool, func(t *testing.T) {
			h := BuildForTest(t)
			ctx := h.OwnerCtx(t, context.Background())
			projectDir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(projectDir, settingsoverlay.DirName()), 0o755); err != nil {
				testutil.FailErr(t, "create project overlay directory", err)
			}
			reviewBody := "review_paths:\n  - path: '**'\n"
			if err := os.WriteFile(filepath.Join(projectDir, settingsoverlay.DirName(), "review.yaml"),
				[]byte(reviewBody), 0o600); err != nil {
				testutil.FailErr(t, "write project review.yaml", err)
			}
			sess, err := h.CreateHarnessSession(t,
				wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, projectDir)
			testutil.FailErr(t, "create test session", err)
			gate := &toolhost.ContentApplyService{
				Mgr:    h.CheckpointMgr,
				Review: mustReviewStore(t, h.ConfigRoot),
			}

			done := make(chan error, 1)
			go func() {
				_, err := gate.GateApply(ctx, tool, "reviewed.txt", nil, "authored bytes", tools.ToolContext{
					Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: projectDir, IsPrimary: true}},
						ActiveRootID: "r1"},
					Identity: tools.InvocationIdentity{SessionID: sess.ID,
						ProjectID: testdbseed.DefaultProjectID},
				})
				done <- err
			}()

			var checkpointID string
			testutil.WaitFor(t, 3*time.Second, func() bool {
				pending, err := h.CheckpointMgr.ListPending(ctx, sess.ID, ptrKind(wire.CheckpointKindContentApply))
				if err == nil && len(pending) == 1 {
					checkpointID = pending[0].ID
					return true
				}
				return false
			})
			if checkpointID == "" {
				t.Fatalf("%s wrote past content review — no checkpoint was raised", tool)
			}
			if _, err := h.CheckpointMgr.ResolveCheckpoint(ctx, sess.ID, checkpointID,
				wire.CheckpointKindContentApply, nil, &hitl.ContentApplyResolve{
					Decision: wire.ContentApplyApprove,
				}); err != nil {
				testutil.FailErr(t, "approve content_apply checkpoint", err)
			}
			if err := <-done; err != nil {
				testutil.FailErr(t, "gate should complete after approval", err)
			}
		})
	}
}

// A tool that authors nothing would raise a card with an empty diff.
func TestContentApplyPassesThroughNonAuthoringTools(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, settingsoverlay.DirName()), 0o755); err != nil {
		testutil.FailErr(t, "create project overlay directory", err)
	}
	reviewBody := "review_paths:\n  - path: '**'\n"
	if err := os.WriteFile(filepath.Join(projectDir, settingsoverlay.DirName(), "review.yaml"),
		[]byte(reviewBody), 0o600); err != nil {
		testutil.FailErr(t, "write project review.yaml", err)
	}
	sess, err := h.CreateHarnessSession(t,
		wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, projectDir)
	testutil.FailErr(t, "create test session", err)
	gate := &toolhost.ContentApplyService{Mgr: h.CheckpointMgr, Review: mustReviewStore(t, h.ConfigRoot)}

	for _, tool := range []string{"copy", "move", "extract_archive", "mkdir", "chmod"} {
		after, err := gate.GateApply(ctx, tool, "reviewed.txt", nil, "bytes", tools.ToolContext{
			Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: projectDir, IsPrimary: true}},
				ActiveRootID: "r1"},
			Identity: tools.InvocationIdentity{SessionID: sess.ID,
				ProjectID: testdbseed.DefaultProjectID},
		})
		testutil.FailErr(t, "gate "+tool, err)
		if after != "bytes" {
			t.Fatalf("%s = %q want passthrough", tool, after)
		}
	}
}

func ptrKind(k wire.CheckpointKind) *wire.CheckpointKind {
	return &k
}

func mustReviewStore(t *testing.T, configRoot string) *settings.ReviewStore {
	t.Helper()
	review, err := settings.NewReviewStoreAt(filepath.Join(configRoot, "review.yaml"))
	testutil.FailErr(t, "open review settings store", err)
	return review
}
