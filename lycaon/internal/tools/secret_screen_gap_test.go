package tools_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// An image the screen could not read raises the ordinary secret card with a
// coverage reason: the standard ladder, the chat release on the face, and the
// redacted send visible but disabled.
func TestUnscreenedImageCardKeepsLadderAndDisablesRedaction(t *testing.T) {
	mgr := &secretScreenHITL{status: hitl.DecisionStatusApproved}
	exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetCheckpointManager(mgr, nil)
	gap := secretmatch.GapOCRUnavailable
	_, err := exec.Secrets.AskSecretScreen(context.Background(), secretmatch.Alert{
		SessionID: "worker", RootSessionID: "sess", ProjectID: "project-id", ProjectDir: "/project",
		Surface:       secretmatch.SurfaceVisualModel,
		DestinationID: string(secretmatch.DestinationModelProvider), DestinationLabel: "model provider",
		RuleID: secretmatch.UnscreenedRuleID, RuleTitle: gap.Label(),
		SourceKind: secretmatch.SourceVisualCapture, SourceTool: "view_image", SourcePath: "shot.png",
		OriginKind:   secretmatch.OriginFile,
		Fingerprints: []secretmatch.SecretFingerprint{secretmatch.UnscreenedFingerprint},
		ScreeningGap: gap,
	})
	testutil.FailErr(t, "ask secret screen", err)
	if mgr.req.SecretScreen == nil || mgr.req.SecretScreen.CanRedact || mgr.req.SecretScreen.ScreeningGap != gap {
		t.Fatalf("secret screen payload = %+v", mgr.req.SecretScreen)
	}
	if !strings.HasPrefix(mgr.req.Title, "Image text could not be screened") {
		t.Fatalf("title = %q", mgr.req.Title)
	}
	var release int
	for _, offer := range mgr.req.GrantOffers {
		if offer.Grant.Predicate.Category == hitl.ApprovalGrantCategorySecretRedact {
			t.Fatalf("unscreened card offered a standing redaction: %+v", offer)
		}
		release++
		if !strings.Contains(offer.Coverage, "images the host could not screen") {
			t.Errorf("rung %q coverage = %q", offer.Rung, offer.Coverage)
		}
	}
	if release != 3 {
		t.Fatalf("release rungs = %d, want day, chat, and project", release)
	}

	plan, err := hitl.CompileCheckpointApprovalPlan(mgr.req)
	testutil.FailErr(t, "compile plan", err)
	var redacted, quiet bool
	for _, opt := range plan.Options {
		switch {
		case opt.Kind == hitl.ApprovalOptionQuiet:
			quiet = true
		case opt.Rung == hitl.ApprovalRungRedacted:
			redacted = true
			if !opt.Disabled || !strings.Contains(opt.Note, "could not read the text") {
				t.Fatalf("redacted option = %+v, want disabled with the host reason", opt)
			}
		}
	}
	if !redacted || quiet {
		t.Fatalf("plan options = %+v", plan.Options)
	}
	face, ok := plan.Option(plan.RecommendedOptionID)
	if !ok || face.Scope != hitl.ApprovalGrantScopeChat {
		t.Fatalf("face = %+v, want the chat release", face)
	}
}
