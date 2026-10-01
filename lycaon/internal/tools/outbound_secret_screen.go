package tools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// AskSecretScreen resolves an outbound hit without persisting its value.
func (e *DefaultToolExecutor) AskSecretScreen(ctx context.Context, finding secretmatch.Alert) (secretmatch.Resolution, error) {
	attr := secretmatch.AskAttributionFrom(ctx)
	finding = fillSecretAttribution(finding, attr)
	fingerprintValues := secretFingerprintValues(finding.Fingerprints)
	canRedact := finding.CanRedact()
	// Host-composed requests redact credentials regardless of destination trust.
	if finding.HostComposed && canRedact {
		e.recordSecretLedger(ctx, finding, authzledger.ActionSecretHostComposedRedacted,
			authzledger.ResolvedBySystemDeny, authzledger.AuthorizationSourceHostComposition)
		return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
	}
	// A cited receipt turns the ask into a redaction contest.
	contested := false
	if token := strings.TrimSpace(finding.ContestToken); token != "" && e != nil {
		_, contested = e.secretReceipts().Consume(secretScreenChatSession(finding), token)
		if !contested {
			// Stale, spent, or over-cap tokens fall through to an ordinary screen.
			finding.ContestToken = ""
		} else {
			e.recordSecretLedger(ctx, finding, authzledger.ActionSecretContest,
				authzledger.ResolvedBySystemDeny, "")
		}
	}
	// Standing redaction applies only to rewritable sends. A trusted destination
	// raises no card, so a contest there cannot reach anyone.
	standingRedaction := e != nil && e.approvalGate != nil &&
		e.approvalGate.SecretRedactionStanding(finding.ProjectID, fingerprintValues)
	if standingRedaction && canRedact && (!contested || finding.DestinationTrusted) {
		return e.redactedResolution(ctx, finding), nil
	}
	posture := e.secretPosture(finding.ProjectDir)
	verdict, decision := evaluateSecretScreen(finding, posture)
	if verdict == gate.Silent {
		switch {
		case finding.DestinationTrusted:
			e.recordSecretLedger(ctx, finding, authzledger.ActionSecretDestinationTrusted,
				authzledger.ResolvedByHuman, authzledger.AuthorizationSourceTrustedDestination)
		case posture.ReleasesChatSecretLocally(secretHit(finding)):
			// A policy release is not a reviewed handoff, so it grants no
			// connection consent to the recipients it names.
			e.recordSecretLedger(ctx, finding, authzledger.ActionSecretChatLocalRelease,
				authzledger.ResolvedByPolicy, authzledger.AuthorizationSourcePolicy)
		}
		return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
	}
	recipients, err := secretScreenRecipients(finding)
	if err != nil {
		return secretmatch.Resolution{}, secretmatch.NewAskFault(secretmatch.FaultStageScreenUnwired, err)
	}
	if !contested && secretInvocationCovered(secretcap.ResolutionFrom(ctx), finding.Fingerprints, recipients) {
		return allowSecretInvocation(ctx, finding, recipients), nil
	}
	if !contested && e != nil && e.approvalGate != nil &&
		e.secretRecipientsCovered(finding, recipients, fingerprintValues) {
		e.recordSecretLedger(ctx, finding, authzledger.ActionSecretPermissionUsed, authzledger.ResolvedByHuman, authzledger.AuthorizationSourceLease)
		return allowSecretInvocation(ctx, finding, recipients), nil
	}
	if e == nil || e.checkpointMgr == nil {
		return secretmatch.Resolution{}, secretmatch.NewAskFault(
			secretmatch.FaultStageCheckpointsUnwired, nil)
	}
	if strings.TrimSpace(finding.SessionID) == "" {
		return secretmatch.Resolution{}, secretmatch.NewAskFault(
			secretmatch.FaultStageNoSession, nil)
	}
	payload := secretReviewPayload(finding, recipients, standingRedaction)
	releaseReview := e.offerSecretIgnoreReview(ctx, finding, payload)
	defer releaseReview()
	args := secretScreenArgs(payload)
	action := hitl.ProposedAction{
		Tool:            payload.Surface,
		Args:            args,
		EstimatedImpact: secretEstimatedImpact(payload),
		SessionID:       finding.SessionID,
		RootSessionID:   finding.RootSessionID,
		ProjectID:       finding.ProjectID,
		ProjectDir:      finding.ProjectDir,
	}
	grantOffers := []hitl.ApprovalGrantOffer(nil)
	if decision != nil && decision.Reuse().Offered() {
		grantOffers = secretReleaseLadder(action.ChatSession(), finding.ProjectID, finding.ProjectDir,
			recipients, finding.SecretNames, finding.Managed(), finding.Fingerprints)
		// Redaction grants remain available at every release ceiling.
		if payload.CanRedact {
			grantOffers = append(grantOffers, secretRedactLadder(finding.ProjectID, finding.ProjectDir, finding.Fingerprints)...)
		}
		// Model requests can offer device-scoped provider trust.
		if finding.Surface == secretmatch.SurfaceModel && strings.TrimSpace(finding.ProviderID) != "" {
			grantOffers = append(grantOffers, trustProviderOffer(finding.ProviderID, finding.DestinationID, payload.DestinationLabel))
		}
	}
	payload.Contested = contested
	actionKey := hitl.GrantKey(action)
	if actionKey == "" {
		return secretmatch.Resolution{}, fmt.Errorf("secret approval action identity cannot be encoded")
	}
	coalesceKey := actionKey + "\x00" + secretmatch.RecipientDigest(recipients)
	switch {
	case len(finding.Fingerprints) > 0:
		coalesceKey += "\x00secret:" + secretmatch.FingerprintDigest(finding.Fingerprints)
	default:
		// Keep fingerprintless findings distinct without carrying secret bytes.
		coalesceKey += "\x00secret-fallback:" + secretScreenFallbackDigest(finding)
	}
	explanation := e.explainSecretScreen(args, payload)
	if len(recipients) > 1 || finding.ScreeningGap != "" {
		explanation.What = hitl.SecretImpact(payload)
	}
	title := fmt.Sprintf("Credential detected before sending this %s", payload.SurfaceLabel)
	if finding.RuleID == secretmatch.UnscreenedRuleID {
		title = fmt.Sprintf("Image text could not be screened before %s", payload.SurfaceLabel)
	}
	if finding.Managed() {
		title = fmt.Sprintf("A protected value will be used in this %s", payload.SurfaceLabel)
	}
	if contested {
		title = fmt.Sprintf("The agent asked to send the real value with this %s", payload.SurfaceLabel)
	}
	final, err := e.raiseAndWaitToolApproval(ctx, toolApprovalRaise{
		Action:                 action,
		Title:                  title,
		ToolCallID:             finding.ToolCallID,
		ProjectID:              finding.ProjectID,
		Explanation:            explanation,
		Decision:               decision,
		GrantOffers:            grantOffers,
		SecretScreenHit:        true,
		SkipGrantOfferAutofill: true,
		SecretScreen:           payload,
		CoalesceKey:            coalesceKey,
	})
	if err != nil {
		return secretmatch.Resolution{}, secretmatch.NewAskFault(secretmatch.FaultStageRaise, err)
	}
	resolution, err := e.resolveSecretScreenDecision(ctx, finding, final)
	if err == nil && resolution.Decision == secretmatch.SendUnchanged {
		return allowSecretInvocation(ctx, finding, recipients), nil
	}
	return resolution, err
}

// secretEstimatedImpact states what the held send may carry.
func secretEstimatedImpact(payload *hitl.SecretScreen) string {
	if payload.RuleID == secretmatch.UnscreenedRuleID {
		return "Outbound request may transmit text the host could not screen"
	}
	return "Outbound request may transmit a secret matching " + payload.RuleTitle
}

func (e *DefaultToolExecutor) resolveSecretScreenDecision(
	ctx context.Context, finding secretmatch.Alert, final *hitl.CheckpointResponse,
) (secretmatch.Resolution, error) {
	if final == nil {
		return secretmatch.Resolution{}, secretmatch.NewAskFault(secretmatch.FaultStageRaise, nil)
	}
	if final.Status == hitl.DecisionStatusApproved {
		if final.Result != nil && final.Result.TrackSecrets {
			return secretmatch.Resolution{Decision: secretmatch.TrackAndReplace}, nil
		}
		if final.Result != nil && final.Result.RedactSecrets {
			// Non-rewritable surfaces cannot accept a redacted decision.
			if !finding.CanRedact() {
				return secretmatch.Resolution{}, secretmatch.NewAskFault(
					secretmatch.FaultStageRedactUnsupported, nil)
			}
			return e.redactedResolution(ctx, finding), nil
		}
		return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
	}
	if final.Status != hitl.DecisionStatusRejected {
		// An unanswered card fails closed and may ask again.
		return secretmatch.Resolution{Decision: secretmatch.Unanswered}, nil
	}
	// Preserve guidance when no tool call exists.
	guidance := ""
	if final.Result != nil {
		guidance = strings.TrimSpace(final.Result.Comments)
	}
	return secretmatch.Resolution{Decision: secretmatch.Withhold, Guidance: guidance}, nil
}

// ResolveSecretScreenUnasked applies recorded redactions without an approval card.
func (e *DefaultToolExecutor) ResolveSecretScreenUnasked(ctx context.Context, finding secretmatch.Alert) (secretmatch.Resolution, error) {
	finding = fillSecretAttribution(finding, secretmatch.AskAttributionFrom(ctx))
	standing := e != nil && e.approvalGate != nil &&
		e.approvalGate.SecretRedactionStanding(finding.ProjectID, secretFingerprintValues(finding.Fingerprints))
	if !standing {
		return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
	}
	// Hold sends when the surface cannot apply required redaction.
	if !finding.CanRedact() {
		return secretmatch.Resolution{}, secretmatch.NewAskFault(
			secretmatch.FaultStageRedactUnsupported, nil)
	}
	return e.redactedResolution(ctx, finding), nil
}

// redactedResolution mints a single-use contest receipt.
func (e *DefaultToolExecutor) redactedResolution(ctx context.Context, finding secretmatch.Alert) secretmatch.Resolution {
	token := ""
	if e != nil {
		token = e.secretReceipts().Mint(secretScreenChatSession(finding), finding.DestinationID, finding.Fingerprints)
		e.recordSecretLedger(ctx, finding, authzledger.ActionSecretReceipt,
			authzledger.ResolvedBySystemDeny, "")
	}
	return secretmatch.Resolution{Decision: secretmatch.SendRedacted, ReceiptToken: token}
}

// recordSecretLedger records outcomes that do not produce a card.
func (e *DefaultToolExecutor) recordSecretLedger(ctx context.Context, finding secretmatch.Alert, action, resolvedBy, authorization string) {
	if e == nil || e.authzRecorder == nil {
		return
	}
	_ = e.authzRecorder.AppendCapabilityRecord(ctx, authzledger.CapabilityRecord{
		SessionID:            secretScreenChatSession(finding),
		ToolCallID:           finding.ToolCallID,
		Action:               action,
		Outcome:              authzledger.OutcomeAllowed,
		ResolvedBy:           resolvedBy,
		AuthorizationSource:  authorization,
		Tool:                 string(finding.Surface),
		DeclaredDestinations: secretRecipientLabels(finding),
	})
}

func secretScreenChatSession(finding secretmatch.Alert) string {
	if root := strings.TrimSpace(finding.RootSessionID); root != "" {
		return root
	}
	return strings.TrimSpace(finding.SessionID)
}

func fillSecretAttribution(finding secretmatch.Alert, attr secretmatch.AskAttribution) secretmatch.Alert {
	if finding.SessionID == "" {
		finding.SessionID = attr.SessionID
	}
	if finding.RootSessionID == "" {
		finding.RootSessionID = attr.RootSessionID
	}
	if finding.ProjectID == "" {
		finding.ProjectID = attr.ProjectID
	}
	if finding.ProjectDir == "" {
		finding.ProjectDir = attr.ProjectDir
	}
	if finding.ToolCallID == "" {
		finding.ToolCallID = attr.ToolCallID
	}
	return finding
}

func secretFingerprintValues(fingerprints []secretmatch.SecretFingerprint) []string {
	values := make([]string, len(fingerprints))
	for i, fingerprint := range fingerprints {
		values[i] = string(fingerprint)
	}
	return values
}

// secretScreenFallbackDigest separates fingerprintless findings by metadata.
func secretScreenFallbackDigest(finding secretmatch.Alert) string {
	parts := []string{
		finding.RuleID, finding.GenericShape, strconv.Itoa(finding.Occurrences),
		string(finding.SourceKind), finding.SourceTool, finding.SourcePath,
		strconv.Itoa(finding.SourceLine), finding.SourceToolCallID, finding.ToolCallID,
		finding.DestinationID, finding.VarName, finding.Container,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func secretScreenArgs(payload *hitl.SecretScreen) map[string]any {
	args := map[string]any{
		"surface":           payload.Surface,
		"surface_label":     payload.SurfaceLabel,
		"can_redact":        payload.CanRedact,
		"destination_id":    payload.DestinationID,
		"destination_label": payload.DestinationLabel,
		"destination_kind":  string(payload.DestinationKind),
		"rule_id":           payload.RuleID,
		"rule_title":        payload.RuleTitle,
		"shape":             payload.GenericShape,
		"occurrences":       payload.Occurrences,
		"source_kind":       payload.SourceKind,
		"source_tool":       payload.SourceTool,
		"source_path":       payload.SourcePath,
		"source_line":       payload.SourceLine,
	}
	if payload.ScreeningGap != "" {
		args["screening_gap"] = string(payload.ScreeningGap)
	}
	return args
}

func (e *DefaultToolExecutor) explainSecretScreen(args map[string]any, screen *hitl.SecretScreen) *hitl.ApprovalExplanation {
	explanation := &hitl.ApprovalExplanation{}
	if e.approvalExplainer != nil {
		copy := e.approvalExplainer.ExplainApproval(hitl.ProposedAction{Tool: outboundSecretExplainTool, Args: args})
		explanation.What = copy.What
		explanation.Who = copy.Who
		explanation.IfWrong = copy.IfWrong
		explanation.AllowLine = copy.AllowLine
	}
	if explanation.What == "" {
		copy := fallbackSecretExplanation(screen)
		explanation.What = copy.What
		explanation.Who = copy.Who
		explanation.IfWrong = copy.IfWrong
		explanation.AllowLine = copy.AllowLine
	}
	return explanation
}

// secretPosture is the project's egress posture, or the default when unwired.
func (e *DefaultToolExecutor) secretPosture(projectDir string) gate.Posture {
	if e == nil || e.egressPostureFor == nil {
		return gate.DefaultPosture
	}
	return e.egressPostureFor(projectDir)
}

// evaluateSecretScreen projects the match into the shared gate table.
func evaluateSecretScreen(finding secretmatch.Alert, posture gate.Posture) (gate.Verdict, *gate.Decision) {
	facts := gate.Facts{Stage: gate.StagePreSend, Ran: gate.ProducerPayload, Payload: secretHit(finding)}
	return gate.Evaluate(facts, posture)
}

// secretHit is the gate's value-free view of a finding.
func secretHit(finding secretmatch.Alert) *gate.SecretHit {
	return &gate.SecretHit{
		Surface:            string(finding.Surface),
		RuleID:             finding.RuleID,
		RuleTitle:          finding.RuleTitle,
		Occurrences:        finding.Occurrences,
		Source:             finding.Source,
		SourceKind:         string(finding.SourceKind),
		SourceTool:         finding.SourceTool,
		DestinationTrusted: finding.DestinationTrusted,
		ChatGenerated:      finding.ChatGenerated,
		RecipientsLocal:    finding.RecipientsLocal,
	}
}

func allowSecretInvocation(ctx context.Context, finding secretmatch.Alert, recipients []secretmatch.Recipient) secretmatch.Resolution {
	resolved := secretcap.ResolutionFrom(ctx)
	resolved.ApproveUse(finding.Fingerprints, recipients)
	resolved.ApproveLocalConnections(finding.ConnectPorts)
	return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}
}

func secretRecipientLabels(finding secretmatch.Alert) []string {
	recipients, err := secretScreenRecipients(finding)
	if err != nil {
		return nil
	}
	labels := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		labels = append(labels, recipient.Label)
	}
	return labels
}
