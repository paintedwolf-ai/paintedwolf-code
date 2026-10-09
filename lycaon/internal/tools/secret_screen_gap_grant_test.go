package tools_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func unscreenedImageAlert(toolCallID, path, destinationID, destinationLabel string) secretmatch.Alert {
	gap := secretmatch.GapOCRUnavailable
	return secretmatch.Alert{
		SessionID: "worker", RootSessionID: "sess", ProjectID: "project-id", ProjectDir: "/project",
		ToolCallID:    toolCallID,
		Surface:       secretmatch.SurfaceVisualModel,
		DestinationID: destinationID, DestinationLabel: destinationLabel,
		RuleID: secretmatch.UnscreenedRuleID, RuleTitle: gap.Label(),
		SourceKind: secretmatch.SourceVisualCapture, SourceTool: "view_image", SourcePath: path,
		OriginKind:   secretmatch.OriginFile,
		Fingerprints: []secretmatch.SecretFingerprint{secretmatch.UnscreenedFingerprint},
		ScreeningGap: gap,
	}
}

// The chat release taken on one unscreened-image card covers later images the
// host could not screen when they go to the same destination in that chat; a
// different destination raises the card again.
func TestUnscreenedImageTaskReleaseCoversLaterImagesToTheSameDestination(t *testing.T) {
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "open approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	mgr := &secretScreenHITL{status: hitl.DecisionStatusApproved}
	exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetCheckpointManager(mgr, gate)
	provider := string(secretmatch.DestinationModelProvider)
	ctx := context.Background()

	first, err := exec.Secrets.AskSecretScreen(ctx, unscreenedImageAlert("call_1", "one.png", provider, "model provider"))
	testutil.FailErr(t, "ask first unscreened image", err)
	if first.Decision != secretmatch.SendUnchanged || mgr.req.SecretScreen == nil {
		t.Fatalf("first image: decision = %q, card = %+v", first.Decision, mgr.req.SecretScreen)
	}
	var task *hitl.ApprovalGrantOffer
	for i, offer := range mgr.req.GrantOffers {
		if offer.Scope == hitl.ApprovalGrantScopeChat && offer.Grant.Predicate.Category == hitl.ApprovalGrantCategorySecret {
			task = &mgr.req.GrantOffers[i]
		}
	}
	if task == nil {
		t.Fatalf("unscreened card offered no task release: %+v", mgr.req.GrantOffers)
	}
	created, err := gate.ApplyGrant(task.Grant)
	testutil.FailErr(t, "apply task release", err)
	if !created {
		t.Fatalf("task release was not recorded: %+v", task.Grant)
	}

	mgr.req = hitl.CheckpointRequest{}
	second, err := exec.Secrets.AskSecretScreen(ctx, unscreenedImageAlert("call_2", "two.png", provider, "model provider"))
	testutil.FailErr(t, "ask second unscreened image", err)
	if second.Decision != secretmatch.SendUnchanged || mgr.req.SecretScreen != nil {
		t.Fatalf("second image to the same destination asked again: decision = %q, card = %+v", second.Decision, mgr.req.SecretScreen)
	}

	mgr.req = hitl.CheckpointRequest{}
	other, err := exec.Secrets.AskSecretScreen(ctx, unscreenedImageAlert("call_3", "three.png", "other_provider", "Other provider"))
	testutil.FailErr(t, "ask unscreened image to another destination", err)
	if mgr.req.SecretScreen == nil {
		t.Fatalf("task release crossed destinations: decision = %q raised no card", other.Decision)
	}
}
