package toolexecution

import (
	"github.com/lycaon/lycaon/internal/toolsecrets"

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
func (e *Secrets) AskSecretScreen(ctx context.Context, finding secretmatch.Alert) (secretmatch.Resolution, error) {
	attr := secretmatch.AskAttributionFrom(ctx)
	finding = fillSecretAttribution(finding, attr)
	fingerprintValues := secretFingerprintValues(finding.Fingerprints)
	canRedact := finding.CanRedact()
	custody := secretcap.ResolutionFrom(ctx).Custody(finding.Fingerprints)
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
	standingRedaction := e != nil && e.Approvals.approvalGate != nil &&
		e.Approvals.approvalGate.SecretRedactionStanding(finding.ProjectID, fingerprintValues)
	if standingRedaction && canRedact && (!contested || finding.DestinationTrusted) {
		return e.redactedResolution(ctx, finding), nil
	}
	posture := e.secretPosture(finding.ProjectDir)
	verdict, decision := evaluateSecretScreen(finding, custody, posture)
	if verdict == gate.Silent {
		switch {
		case finding.DestinationTrusted:
			e.recordSecretLedger(ctx, finding, authzledger.ActionSecretDestinationTrusted,
				authzledger.ResolvedByHuman, authzledger.AuthorizationSourceTrustedDestination)
		case posture.ReleasesChatSecretLocally(secretHit(finding, custody)):
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
	// A value a person holds leaves only while their chat is unlocked; a
	// device that cannot verify presence can never unlock it.
	if len(custody.Held) > 0 && !e.presenceAvailable() {
		return secretmatch.Resolution{}, secretmatch.NewAskFault(secretmatch.FaultStagePresenceUnavailable, nil)
	}
	if !contested && secretInvocationCovered(secretcap.ResolutionFrom(ctx), finding.Fingerprints, recipients) {
		return e.ensureHeldUnlocked(ctx, finding, custody, recipients)
	}
	if !contested && e != nil && e.Approvals.approvalGate != nil && e.secretRecipientsCovered(finding, recipients, fingerprintValues) {
		e.recordSecretLedger(ctx, finding, authzledger.ActionSecretPermissionUsed, authzledger.ResolvedByHuman, authzledger.AuthorizationSourceLease)
		allowSecretInvocation(ctx, finding, recipients)
		return e.ensureHeldUnlocked(ctx, finding, custody, recipients)
	}
	if e == nil || e.Approvals.checkpointMgr == nil {
		return secretmatch.Resolution{}, secretmatch.NewAskFault(
			secretmatch.FaultStageCheckpointsUnwired, nil)
	}
	if strings.TrimSpace(finding.SessionID) == "" {
		return secretmatch.Resolution{}, secretmatch.NewAskFault(
			secretmatch.FaultStageNoSession, nil)
	}
	payload := toolsecrets.SecretReviewPayload(finding, recipients, standingRedaction)
	payload.Held = heldRelease(finding, custody, recipients)
	releaseReview := e.offerSecretIgnoreReview(ctx, finding, payload)
	defer releaseReview()
	args := secretScreenArgs(payload)
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: payload.Surface,
Args: args,
},
Presentation: hitl.ActionPresentation{
EstimatedImpact: secretEstimatedImpact(payload),
},
Scope: hitl.ActionScope{
SessionID: finding.SessionID,
RootSessionID: finding.RootSessionID,
ProjectID: finding.ProjectID,
ProjectDir: finding.ProjectDir,
},
}
	grantOffers := []hitl.ApprovalGrantOffer(nil)
	if decision != nil && decision.Reuse().Offered() {
		grantOffers = secretReleaseLadder(action.Scope.ChatSession(), finding.ProjectID, finding.ProjectDir,
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
	if payload.Held != nil {
		title = fmt.Sprintf("A value you stored will be used in this %s", payload.SurfaceLabel)
	}
	if contested {
		title = fmt.Sprintf("The agent asked to send the real value with this %s", payload.SurfaceLabel)
	}
	final, err := e.Approvals.raiseAndWaitToolApproval(ctx, toolApprovalRaise{
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
		// Approving unlocked the chat if it was locked.
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

func (e *Secrets) resolveSecretScreenDecision(
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

// ResolveSecretScreenUnasked applies recorded redactions without an approval
// card. Custody is not an approval preference, so a value a person holds is
// still asked about with approvals disabled.
func (e *Secrets) ResolveSecretScreenUnasked(ctx context.Context, finding secretmatch.Alert) (secretmatch.Resolution, error) {
	if len(secretcap.ResolutionFrom(ctx).Custody(finding.Fingerprints).Held) > 0 {
		return e.AskSecretScreen(ctx, finding)
	}
	finding = fillSecretAttribution(finding, secretmatch.AskAttributionFrom(ctx))
	standing := e != nil && e.Approvals.approvalGate != nil &&
		e.Approvals.approvalGate.SecretRedactionStanding(finding.ProjectID, secretFingerprintValues(finding.Fingerprints))
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
func (e *Secrets) redactedResolution(ctx context.Context, finding secretmatch.Alert) secretmatch.Resolution {
	token := ""
	if e != nil {
		token = e.secretReceipts().Mint(secretScreenChatSession(finding), finding.DestinationID, finding.Fingerprints)
		e.recordSecretLedger(ctx, finding, authzledger.ActionSecretReceipt,
			authzledger.ResolvedBySystemDeny, "")
	}
	return secretmatch.Resolution{Decision: secretmatch.SendRedacted, ReceiptToken: token}
}

// recordSecretLedger records outcomes that do not produce a card.
func (e *Secrets) recordSecretLedger(ctx context.Context, finding secretmatch.Alert, action, resolvedBy, authorization string) {
	if e == nil || e.Approvals.authzRecorder == nil {
		return
	}
	_ = e.Approvals.authzRecorder.AppendCapabilityRecord(ctx, authzledger.CapabilityRecord{
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

func (e *Secrets) explainSecretScreen(args map[string]any, screen *hitl.SecretScreen) *hitl.ApprovalExplanation {
	explanation := &hitl.ApprovalExplanation{}
	if e.Approvals.approvalExplainer != nil {
		copy := e.Approvals.approvalExplainer.ExplainApproval(hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: toolsecrets.OutboundSecretExplainTool,
Args: args,
},
})
		explanation.What = copy.What
		explanation.Who = copy.Who
		explanation.IfWrong = copy.IfWrong
		explanation.AllowLine = copy.AllowLine
	}
	if explanation.What == "" {
		copy := toolsecrets.FallbackSecretExplanation(screen)
		explanation.What = copy.What
		explanation.Who = copy.Who
		explanation.IfWrong = copy.IfWrong
		explanation.AllowLine = copy.AllowLine
	}
	return explanation
}

// secretPosture is the project's egress posture, or the default when unwired.
func (e *Secrets) secretPosture(projectDir string) gate.Posture {
	if e == nil || e.Network.egressPostureFor == nil {
		return gate.DefaultPosture
	}
	return e.Network.egressPostureFor(projectDir)
}

// evaluateSecretScreen projects the match into the shared gate table.
func evaluateSecretScreen(finding secretmatch.Alert, custody secretcap.CustodySummary, posture gate.Posture) (gate.Verdict, *gate.Decision) {
	facts := gate.Facts{Stage: gate.StagePreSend, Ran: gate.ProducerPayload, Payload: secretHit(finding, custody)}
	return gate.Evaluate(facts, posture)
}

// secretHit is the gate's value-free view of a finding and the custody of the
// values its invocation resolved.
func secretHit(finding secretmatch.Alert, custody secretcap.CustodySummary) *gate.SecretHit {
	return &gate.SecretHit{
		Surface:            string(finding.Surface),
		RuleID:             finding.RuleID,
		RuleTitle:          finding.RuleTitle,
		Occurrences:        finding.Occurrences,
		Source:             finding.Source,
		SourceKind:         string(finding.SourceKind),
		SourceTool:         finding.SourceTool,
		DestinationTrusted: finding.DestinationTrusted,
		ChatGenerated:      custody.ChatGenerated,
		Held:               len(custody.Held) > 0,
		RecipientsLocal:    finding.RecipientsLocal,
	}
}

// allowSecretInvocation records the reviewed release on the invocation.
func allowSecretInvocation(ctx context.Context, finding secretmatch.Alert, recipients []secretmatch.Recipient) secretmatch.Resolution {
	resolved := secretcap.ResolutionFrom(ctx)
	resolved.ApproveRelease(secretcap.Release{Fingerprints: finding.Fingerprints, Recipients: recipients})
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

// HeldHandOffReject refuses a consumer that would receive a person-held
// value without a reviewed release, or while its chat is locked.

func (e *Secrets) SetSecretExposureSource(fn func(ctx context.Context, chatSessionID string) (bool, error)) {
	if e != nil {
		e.secretExposure = fn
	}
}

func (e *Secrets) SetUntrustedIngestionSource(fn func(ctx context.Context, chatSessionID string) (bool, error)) {
	if e != nil {
		e.untrustedIngestion = fn
	}
}
