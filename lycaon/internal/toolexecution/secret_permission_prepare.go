package toolexecution

import (
	"github.com/lycaon/lycaon/internal/capabilityrequest"

	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// prepareSecretPermission builds the managed-secret release that joins an
// action card; raw detections wait for the final payload screen.
func (e *Secrets) prepareSecretPermission(ctx context.Context, tool string, args map[string]any, tc tools.ToolContext) (permission *hitl.SecretPermission, failure error) {
	declared := tc.Invocation.Contract.SecretReferenceSurface
	surface := secretmatch.ScreenSurface(declared)
	defer func() {
		if failure == nil {
			return
		}
		stage := secretmatch.FaultStageScreenUnwired
		if fault, ok := secretmatch.Faulted(failure); ok {
			stage = fault.Stage
		}
		failure = &toolrejection.ToolReject{Code: toolrejection.OutboundSecretScreenFailedCode, Data: map[string]any{
			"surface": string(surface), "rule_id": secretmatch.ManagedRuleID, "shape": "Protected value", "fault_stage": stage,
		}}
	}()
	if e.Capabilities.approvalsDisabled != nil && e.Capabilities.approvalsDisabled(tc.ActiveRootPath()) {
		return nil, nil
	}
	included := func(string) bool { return true }
	var destination, label string
	var local bool
	switch {
	case declared.ProcessArguments():
		destination, label = argvSecretDestination(tc)
		if request, _ := capabilityrequest.ParseCapabilityRequest(args); request != nil && request.DirectIP != nil {
			predicted := tc
			predicted.Direct.DirectIPAuthorized = true
			destination, label = argvSecretDestination(predicted)
		}
		local = argvSecretRecipientsLocal(ctx)
	case declared == toolcontract.SecretSurfaceHTTPRequest:
		included = func(path string) bool { return secretmatch.HTTPArgumentConsumed(args, path) }
		address, _ := args["url"].(string)
		target, err := outboundhttp.NormalizeURL(address)
		if err != nil {
			return nil, nil
		}
		// Only the request screen knows the dialed address, so a composed
		// card never treats an HTTP recipient as local.
		destination, label = secretmatch.HTTPDestination(target)
	case declared == toolcontract.SecretSurfaceFile:
		filePath, _ := args["path"].(string)
		destination, label = fileSecretDestination(filePath)
		local = true
	default:
		return nil, nil
	}
	known, err := tc.Effects.Secrets.Matches(e.secretMatcher, included)
	if err != nil || len(known) == 0 {
		return nil, err
	}
	finding := argvSecretFinding(surface, tool, tc, known[0], known)
	finding.DestinationID, finding.DestinationLabel = destination, label
	finding.Recipients, finding.SecretNames = secretUseFrom(ctx), secretmatch.ManagedNames(known)
	recipients, err := secretScreenRecipients(finding)
	if err != nil {
		return nil, err
	}
	custody := tc.Effects.Secrets.Custody(finding.Fingerprints)
	if secretInvocationCovered(tc.Effects.Secrets, finding.Fingerprints, recipients) {
		return nil, nil
	}
	// A covered held value still needs its chat unlocked; the final payload
	// screen asks for that.
	if e.Approvals.approvalGate != nil && e.secretRecipientsCovered(finding, recipients, secretFingerprintValues(finding.Fingerprints)) {
		return nil, nil
	}
	// Standing redaction is applied by the final payload screen.
	if e.Approvals.approvalGate != nil && e.Approvals.approvalGate.SecretRedactionStanding(tc.Identity.ProjectID, secretFingerprintValues(finding.Fingerprints)) {
		return nil, nil
	}
	// A release the gate makes silently joins no card; the final payload
	// screen records it.
	finding.RecipientsLocal = local
	posture := e.secretPosture(tc.ActiveRootPath())
	if verdict, _ := evaluateSecretScreen(finding, custody, posture); verdict == gate.Silent {
		return nil, nil
	}
	if len(custody.Held) > 0 && !e.presenceAvailable() {
		return nil, secretmatch.NewAskFault(secretmatch.FaultStagePresenceUnavailable, nil)
	}
	offers := secretReleaseLadder(secretScreenChatSession(finding), tc.Identity.ProjectID, tc.ActiveRootPath(), recipients,
		finding.SecretNames, true, finding.Fingerprints)
	if len(offers) == 0 {
		return nil, nil
	}
	return &hitl.SecretPermission{
		Screen: hitl.SecretScreen{
			Surface: string(surface), SurfaceLabel: surface.Label(), Managed: true,
			DestinationID: destination, DestinationLabel: label, DestinationKind: surface.DestinationKind(),
			Recipients: recipients, SecretNames: finding.SecretNames,
			SourcePath: "arguments", OriginKind: secretmatch.OriginField, ToolCallID: tc.Identity.ToolCallID,
			Held: heldRelease(finding, custody, recipients),
		},
		Offers: offers, Fingerprints: finding.Fingerprints,
		ConnectPorts: append([]uint16(nil), secretUseConnectPorts(ctx)...),
	}, nil
}

func secretInvocationCovered(resolution *secretcap.Resolution, fingerprints []secretmatch.SecretFingerprint, recipients []secretmatch.Recipient) bool {
	if len(fingerprints) == 0 || len(recipients) == 0 {
		return false
	}
	for _, recipient := range recipients {
		for _, fingerprint := range fingerprints {
			if !resolution.UseCovered(fingerprint, recipient) {
				return false
			}
		}
	}
	return true
}

// approveSecretPermission records a composed card's release.
func approveSecretPermission(resolution *secretcap.Resolution, permission *hitl.SecretPermission) {
	if permission != nil {
		resolution.ApproveRelease(secretcap.Release{Fingerprints: permission.Fingerprints, Recipients: permission.Screen.Recipients})
		resolution.ApproveLocalConnections(permission.ConnectPorts)
	}
}

// composeToolSecretReview joins disclosure to an ordinary action approval.
func (e *Secrets) composeToolSecretReview(ctx context.Context, review *toolApprovalRaise, tc tools.ToolContext) (*hitl.SecretPermission, error) {
	permission, err := e.prepareSecretPermission(ctx, review.Action.Tool, review.Action.Args, tc)
	if err != nil || permission == nil {
		return permission, err
	}
	plan, err := hitl.CompileCheckpointApprovalPlan(hitl.CheckpointRequest{
		ProposedAction: &review.Action, Title: review.Title, Decision: review.Decision,
		Explanation: review.Explanation, GrantOffers: review.GrantOffers, GrantDelta: review.GrantDelta,
		Detection: review.Detection, ApprovalMatches: review.ApprovalMatches,
	})
	if err != nil {
		return nil, toolrejection.ApprovalPlanInvalid()
	}
	plan, err = hitl.ComposeSecretPermission(plan, review.Action, permission, hitl.FaceContext{})
	if err != nil {
		return nil, toolrejection.ApprovalPlanInvalid()
	}
	review.Plan = plan
	review.SecretScreenHit = true
	actionKey := hitl.GrantKey(review.Action)
	if actionKey == "" {
		return nil, toolrejection.ApprovalPlanInvalid()
	}
	review.CoalesceKey = permission.Key(actionKey)
	return permission, nil
}
