package toolexecution

import (
	"context"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func invocationContext(ctx context.Context, tc tools.ToolContext) context.Context {
	ctx = people.WithoutCaller(ctx)
	ctx = tools.WithRecoveryTools(ctx, tc.Turn.TurnOfferedToolNames)
	ctx = tools.SandboxScopeContext(ctx, tc)
	ctx = authzledger.WithInvocation(ctx, tc.Identity.SessionID, tc.Identity.ParentSessionID, tc.Identity.ToolCallID)
	sessionFacts := curationctx.SessionFrom(ctx)
	ctx = curationctx.WithSession(ctx, curationctx.Session{
		SessionID:       tc.Identity.SessionID,
		OwnerPersonID:   sessionFacts.OwnerPersonID,
		Posture:         sessionFacts.Posture,
		ProjectID:       tc.Identity.ProjectID,
		Agent:           tc.Identity.Agent,
		ParentSessionID: tc.Identity.ParentSessionID,
		ToolCallID:      tc.Identity.ToolCallID,
		ProjectDir:      tc.ActiveRootPath(),
	})
	return ctx
}

// prepareOwnerDispatch shares the complete final screening/review phase between
// structured and typed native interfaces. Owners receive the resolved arguments
// that passed the existing boundary, rather than the adapter's earlier input.
func (e *Executor) prepareOwnerDispatch(ctx context.Context, qualifiedName, profileID string, canonicalArgs map[string]any, tc *tools.ToolContext) (context.Context, map[string]any, error) {
	// Handlers execute against resolved arguments and echo against these.
	tc.Effects.CanonicalArgs = canonicalArgs
	ctx = secretcap.WithResolution(ctx, tc.Effects.Secrets)
	executionArgs := tc.Effects.Secrets.Arguments
	if err := e.Secrets.screenArgvSecrets(ctx, qualifiedName, executionArgs, *tc); err != nil {
		return ctx, nil, err
	}
	if err := e.Secrets.screenFileSecrets(ctx, qualifiedName, executionArgs, *tc); err != nil {
		return ctx, nil, err
	}
	// The screened save is a file tool's transport.
	if tc.Invocation.Contract.SecretReferenceSurface.IsFile() {
		if err := tc.Effects.Secrets.HandOff(ctx, nil); err != nil {
			return ctx, nil, e.Rejections.rejectBeforeInvoke(ctx, qualifiedName, profileID, canonicalArgs, toolrejection.HeldHandOffReject(string(secretmatch.SurfaceFile), err))
		}
	}
	tc.Execution.ProcessReview = e.Process.processReviewer(qualifiedName, canonicalArgs, *tc)
	tc.Files.FileChangeReview = e.Boundary.fileChangeReviewer(qualifiedName, canonicalArgs, *tc)
	if e.Rejections.blockPlane != nil {
		if err := e.Rejections.blockPlane.Evaluate(ctx, oar.AnchorToolPreInvoke, qualifiedName, profileID, canonicalArgs, nil); err != nil {
			return ctx, nil, err
		}
		if err := e.Rejections.blockPlane.Evaluate(ctx, oar.AnchorToolHandler, qualifiedName, profileID, canonicalArgs, nil); err != nil {
			return ctx, nil, err
		}
	}
	return ctx, executionArgs, nil
}
