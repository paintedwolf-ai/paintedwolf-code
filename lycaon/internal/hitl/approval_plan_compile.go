package hitl

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/pkg/api"
)

// CompileCheckpointApprovalPlan composes every contribution already resolved
// for a checkpoint into the single plan persisted, rendered and applied.
func CompileCheckpointApprovalPlan(req CheckpointRequest) (*ApprovalPlan, error) {
	if req.ApprovalPlan != nil {
		return req.ApprovalPlan, nil
	}
	if req.ProposedAction == nil {
		return nil, fmt.Errorf("proposed action required")
	}
	if req.Decision == nil {
		return nil, fmt.Errorf("gate decision required")
	}
	action := *req.ProposedAction
	displayAction := redactedApprovalAction(action)
	title := observability.RedactCaptureText(req.Title)
	stage := ApprovalStagePreSpawn
	subject := genericApprovalSubject(displayAction, title)
	primaryGate, cited, reasons := PresentDecision(req.Decision)
	presentation := ApprovalPresentation{
		FileChanges: redactedFileChanges(action.FileChanges),
		Action:      actionLabel(displayAction), Tool: firstNonEmpty(displayAction.PresentationTool, displayAction.Tool), Command: displayAction.Command,
		Impact: firstNonEmpty(observability.RedactCaptureText(action.EstimatedImpact), "Allow the agent to perform this exact action."),
		Lead:   leadFact(primaryGate, cited),
		Gate:   primaryGate, Cited: cited, GrantDelta: req.GrantDelta,
		ConsequenceBand: string(req.ConsequenceBand), ConsequenceCode: string(req.ConsequenceCode),
		Detection:     req.Detection,
		ApprovalRules: append([]ApprovalRuleMatch(nil), req.ApprovalMatches...),
	}
	if req.Explanation != nil {
		presentation.Impact = observability.RedactCaptureText(req.Explanation.What)
		presentation.Who = observability.RedactCaptureText(req.Explanation.Who)
		presentation.IfWrong = observability.RedactCaptureText(req.Explanation.IfWrong)
		presentation.AllowLine = observability.RedactCaptureText(req.Explanation.AllowLine)
	}

	options := approvalOptionsFromOffers(req.GrantOffers, true)
	switch {
	case action.PackageExecution != nil:
		subject = packageExecutionSubject(displayAction, title)
	case req.SocketCapability != nil:
		subject = socketApprovalSubject(title, req.SocketCapability.Targets)
		options = socketApprovalOptions(req, action)
	case req.DirectIPCapability != nil:
		subject = directIPApprovalSubject(title, req.DirectIPCapability)
		directIPOptions, err := directIPApprovalOptions(req)
		if err != nil {
			return nil, err
		}
		options = directIPOptions
	case req.DeclaredEndpoints != nil:
		stage = ApprovalStagePreDial
		subject = destinationSetApprovalSubject(title, req.DeclaredEndpoints.Hosts)
	case req.SecretScreen != nil:
		presentation.IgnoreCandidateID = req.SecretScreen.IgnoreCandidateID
		stage = ApprovalStagePreSend
		subject = secretApprovalSubject(title, req.SecretScreen)
		options = attachSecretServiceConnections(action, req.SecretScreen, secretApprovalOptions(req.SecretScreen, req.GrantOffers))
		// Secret cards render the reviewed surface label.
		if label := strings.TrimSpace(req.SecretScreen.SurfaceLabel); label != "" {
			presentation.Action = "Send " + label
			presentation.Tool = label
		}
		if !req.SecretScreen.CanRedact {
			presentation.OptionNote = strings.TrimSpace(req.SecretScreen.RedactionNote)
			// The note also names a held grant that could not answer.
			if req.SecretScreen.StandingRedactionHeld {
				presentation.OptionNote = strings.TrimSpace(
					presentation.OptionNote + " " + NoteStandingRedactionHeld,
				)
			}
		}
		if req.SecretScreen.Contested {
			presentation.OptionNote = strings.TrimSpace(
				NoteContestedRedaction + " " + presentation.OptionNote,
			)
		}
		if cmd := strings.TrimSpace(req.SecretScreen.CommandLine); cmd != "" {
			presentation.Command = cmd
		}
		presentation.Location = compileSecretLocation(req.SecretScreen)
	case action.Contained.HostExecution:
		subject = ApprovalSubject{
			Kind:    ApprovalSubjectHostExecution,
			Title:   firstNonEmpty(title, HostExecutionTitle),
			Targets: []ApprovalTarget{{Kind: string(ApprovalSubjectHostExecution), Label: action.Command}},
		}
		presentation.Impact = HostExecutionWhat
		presentation.IfWrong = HostExecutionIfWrong
		presentation.AllowLine = ExecutionAllowLine
	case action.Contained.ProcessControl:
		subject = ApprovalSubject{
			Kind:    ApprovalSubjectProcessControl,
			Title:   firstNonEmpty(title, ProcessControlTitle),
			Targets: []ApprovalTarget{{Kind: string(ApprovalSubjectProcessControl), Label: action.Command}},
		}
		presentation.Impact = ProcessControlWhat
		presentation.IfWrong = ProcessControlIfWrong
		presentation.AllowLine = ExecutionAllowLine
	}

	skipQuiet := map[string]struct{}{}
	for _, key := range req.QuietSkipKeys {
		if key = strings.TrimSpace(key); key != "" {
			skipQuiet[key] = struct{}{}
		}
	}
	options = append(options, QuietOptions(action, req.Decision, req.SecretScreen, func(key string) bool {
		_, ok := skipQuiet[key]
		return ok
	})...)
	if req.SecretScreen != nil && len(req.SecretScreen.Recipients) > 1 {
		for i := range options {
			if options[i].Kind == ApprovalOptionQuiet {
				options[i].Disabled = true
				options[i].Note = "Choose a duration that covers the protected values and every named recipient."
			}
		}
	}

	if len(options) == 0 {
		return nil, fmt.Errorf("approval plan has no valid affirmative option")
	}
	face := FaceContext{}
	if req.SecretScreen != nil {
		face.SecretManaged = req.SecretScreen.Managed
	}
	return NewApprovalPlan(action, stage, subject, presentation, reasons, options, face)
}

// leadFactKeys orders the facts shown on the approval card.
var leadFactKeys = []string{"destination.state", "session.state", "effect.class"}

// leadFact lifts the primary gate's most distinguishing citation onto the face.
func leadFact(primary api.ApprovalGate, cited []PresentedFact) string {
	for _, key := range leadFactKeys {
		for _, fact := range cited {
			if fact.Gate == primary && fact.Key == key && strings.TrimSpace(fact.Value) != "" {
				return sentenceCase(fact.Value)
			}
		}
	}
	return ""
}

func sentenceCase(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

func redactedApprovalAction(action ProposedAction) ProposedAction {
	action.Tool = observability.RedactCaptureText(action.Tool)
	action.PresentationTool = observability.RedactCaptureText(action.PresentationTool)
	action.Command = observability.RedactCaptureText(action.Command)
	action.EstimatedImpact = observability.RedactCaptureText(action.EstimatedImpact)
	action.ProjectDir = observability.RedactCaptureText(action.ProjectDir)
	action.Files = append([]string(nil), action.Files...)
	for i := range action.Files {
		action.Files[i] = observability.RedactCaptureText(action.Files[i])
	}
	action.DeclaredDestinations = append([]string(nil), action.DeclaredDestinations...)
	for i := range action.DeclaredDestinations {
		action.DeclaredDestinations[i] = observability.RedactCaptureText(action.DeclaredDestinations[i])
	}
	if scrubbed, ok := observability.RedactCaptureValue(action.Args).(map[string]any); ok {
		action.Args = scrubbed
	}
	return action
}

// redactedFileChanges screens the text and paths a person reads. Sizes and
// hashes stay exact, so the review still names the bytes the digest bound.
func redactedFileChanges(changes []api.ApprovalFileChange) []api.ApprovalFileChange {
	if len(changes) == 0 {
		return nil
	}
	out := make([]api.ApprovalFileChange, len(changes))
	for i, change := range changes {
		change.Path = observability.RedactCaptureText(change.Path)
		change.FromPath = observability.RedactCaptureText(change.FromPath)
		change.Before = observability.RedactCaptureText(change.Before)
		change.After = observability.RedactCaptureText(change.After)
		out[i] = change
	}
	return out
}

func packageExecutionSubject(action ProposedAction, title string) ApprovalSubject {
	execution := action.PackageExecution
	if execution == nil {
		return genericApprovalSubject(action, title)
	}
	targets := make([]ApprovalTarget, 0, len(execution.Packages))
	boundary := confine.NewRemotePackageExecutionBoundary(execution.AllowedHosts, execution.ApprovedReadPaths)
	for _, pkg := range execution.Packages {
		version := strings.TrimSpace(pkg.ResolvedVersion)
		if version == "" {
			version = strings.TrimSpace(pkg.RequestedVersion)
		}
		label := strings.TrimSpace(pkg.Name)
		if version != "" {
			label += "@" + version
		}
		details := map[string]any{
			"system": pkg.System, "manager": execution.Manager,
			"operation": execution.Operation, "identity_status": pkg.Status,
			"ambient_credentials_removed": boundary.AmbientCredentialsRemoved,
			"protected_reads_denied":      boundary.ProtectedReadsDenied,
			"approved_read_paths":         boundary.ApprovedReadPaths,
			"network_scope":               boundary.NetworkScope,
		}
		if len(boundary.AllowedHosts) > 0 {
			details["allowed_hosts"] = append([]string(nil), boundary.AllowedHosts...)
		}
		if pkg.RequestedVersion != "" {
			details["requested_version"] = pkg.RequestedVersion
		}
		if pkg.ResolvedVersion != "" {
			details["resolved_version"] = pkg.ResolvedVersion
		}
		if pkg.PublishedAt != nil {
			details["published_at"] = pkg.PublishedAt.Format(time.RFC3339)
		}
		if pkg.AgeDays != nil {
			details["age_days"] = *pkg.AgeDays
		}
		if pkg.Registry != "" {
			details["registry"] = pkg.Registry
		}
		if pkg.SourceRepository != "" {
			details["source_repository"] = pkg.SourceRepository
		}
		if pkg.VerifiedAttestation {
			details["verified_attestation"] = true
		}
		if pkg.StatusDetail != "" {
			details["identity_detail"] = pkg.StatusDetail
		}
		targets = append(targets, ApprovalTarget{Kind: "package", Label: label, Details: details})
	}
	return ApprovalSubject{
		Kind:    ApprovalSubjectPackageSet,
		Title:   firstNonEmpty(title, "Run downloaded package code"),
		Summary: "The exact package identity and the downloaded-code boundary are shown before this action runs.",
		Targets: targets,
	}
}

func socketApprovalOptions(req CheckpointRequest, action ProposedAction) []ApprovalOption {
	targets := make([]ApprovalSocketTarget, 0, len(req.SocketCapability.Targets))
	for _, target := range req.SocketCapability.Targets {
		targets = append(targets, ApprovalSocketTarget{
			ApprovedPath: target.ApprovedPath,
			ResolvedPath: target.ResolvedPath,
		})
	}
	permit := ApprovalAuthorityDelta{
		Kind: AuthoritySocketPermit, SessionID: req.SessionID, ToolCallID: req.ToolCallID,
		ActionDigest: GrantKey(action), Sockets: targets,
	}
	once := ApprovalOption{
		ID: "approve_socket_set_once", Kind: ApprovalOptionCurrentAction, Rung: ApprovalRungOnce,
		Title: TitleAllowOnce, Coverage: "only these local services for this action",
		ExpiresWhen: ExpiresAfterThisAction, ReaskWhen: "a later action requests these services",
		DecisionAction: ApprovalOptionApprove, Authority: []ApprovalAuthorityDelta{permit},
	}
	options := []ApprovalOption{once}
	for _, offer := range req.GrantOffers {
		options = append(options, ContinuingLeaseOption(offer, permit))
	}
	return options
}

func directIPApprovalOptions(req CheckpointRequest) ([]ApprovalOption, error) {
	var lease *DirectIPLease
	for _, offer := range req.GrantOffers {
		for _, authority := range offer.Authority {
			if authority.DirectIPLease != nil && authority.DirectIPLease.Complete() {
				copy := *authority.DirectIPLease
				lease = &copy
				break
			}
		}
		if lease != nil {
			break
		}
	}
	if lease == nil {
		return nil, fmt.Errorf("direct-IP approval requires a complete host lease")
	}
	permit := ApprovalAuthorityDelta{
		Kind: AuthorityDirectIPPermit, SessionID: req.SessionID, ToolCallID: req.ToolCallID,
		ActionDigest: req.DirectIPCapability.ActionDigest, DirectIPLease: lease,
	}
	once := ApprovalOption{
		ID: "approve_direct_ip_once", Kind: ApprovalOptionCurrentAction, Rung: ApprovalRungOnce,
		Title: TitleAllowOnce, Coverage: "only this exact direct-network action",
		ExpiresWhen: ExpiresAfterThisAction, ReaskWhen: ReaskWhenActionRunsAgain,
		DecisionAction: ApprovalOptionApprove, Authority: []ApprovalAuthorityDelta{permit},
	}
	options := []ApprovalOption{once}
	for _, offer := range req.GrantOffers {
		options = append(options, ContinuingLeaseOption(offer, permit))
	}
	return options, nil
}

func approvalOptionsFromOffers(offers []ApprovalGrantOffer, includeCurrent bool) []ApprovalOption {
	options := make([]ApprovalOption, 0, len(offers)+1)
	if includeCurrent {
		options = append(options, CurrentActionOption())
	}
	for _, offer := range offers {
		options = append(options, GrantOption(offer))
	}
	return options
}

func secretApprovalOptions(secret *SecretScreen, offers []ApprovalGrantOffer) []ApprovalOption {
	options := make([]ApprovalOption, 0, len(offers)+2)
	if secret.CanTrack && !secret.Contested {
		options = append(options, TrackAndReplaceOption())
	}
	// Contested cards keep the redacted send disabled, in place.
	if secret.CanRedact && !secret.Contested {
		if secret.RedactionBreaks {
			options = append(options, BreakingSendRedactedOption())
		} else {
			options = append(options, SendRedactedOption())
		}
	} else {
		note := strings.TrimSpace(secret.RedactionNote)
		if secret.Contested {
			note = NoteContestedRedaction
		}
		options = append(options, UnavailableSendRedactedOption(note))
	}
	options = append(options, secretSendOption(secret.Managed))
	for _, offer := range offers {
		options = append(options, GrantOption(offer))
	}
	return options
}

func genericApprovalSubject(action ProposedAction, title string) ApprovalSubject {
	label := strings.TrimSpace(action.Command)
	if label == "" {
		if len(action.Files) > 0 {
			label = strings.Join(action.Files, "\n")
		} else if encoded, err := json.Marshal(action.Args); err == nil && string(encoded) != "{}" {
			label = string(encoded)
		}
	}
	if label == "" {
		label = action.Tool
	}
	return ApprovalSubject{
		Kind: ApprovalSubjectAction, Title: firstNonEmpty(title, "Approve "+action.Tool),
		Targets: []ApprovalTarget{{Kind: "action", Label: label, Details: map[string]any{"tool": action.Tool, "args": action.Args}}},
	}
}

func socketApprovalSubject(title string, sockets []SocketCapabilityTarget) ApprovalSubject {
	targets := make([]ApprovalTarget, 0, len(sockets))
	for _, socket := range sockets {
		targets = append(targets, ApprovalTarget{Kind: "socket", Label: socket.ApprovedPath, Details: map[string]any{
			"approved_path": socket.ApprovedPath, "resolved_path": socket.ResolvedPath,
			"already_chat_granted": socket.AlreadyChatGranted,
		}})
	}
	return ApprovalSubject{Kind: ApprovalSubjectSocketSet, Title: firstNonEmpty(title, "Allow local services"), Targets: targets}
}

func directIPApprovalSubject(title string, direct *DirectIPCapability) ApprovalSubject {
	label := strings.TrimSpace(observability.RedactCaptureText(direct.CommandSummary))
	if label == "" {
		label = "Direct outbound networking"
	}
	return ApprovalSubject{Kind: ApprovalSubjectDirectIP, Title: firstNonEmpty(title, "Allow direct network access"), Targets: []ApprovalTarget{{
		Kind: "direct_ip", Label: label, Details: map[string]any{
			"visibility": direct.Visibility, "af_unix": direct.AFUnix,
			"declared_destinations": direct.DeclaredDestinations,
		},
	}}}
}

func destinationSetApprovalSubject(title string, hosts []string) ApprovalSubject {
	targets := make([]ApprovalTarget, 0, len(hosts))
	for _, host := range hosts {
		targets = append(targets, ApprovalTarget{Kind: "destination", Label: host})
	}
	return ApprovalSubject{Kind: ApprovalSubjectDestinationSet, Title: firstNonEmpty(title, "Allow configured endpoints"), Targets: targets}
}

func secretApprovalSubject(title string, secret *SecretScreen) ApprovalSubject {
	details := map[string]any{}
	if shape := strings.TrimSpace(secret.GenericShape); shape != "" {
		details[secretGenericShapeDetail] = shape
	}
	if secret.ScreeningGap != "" {
		details[secretScreeningGapDetail] = string(secret.ScreeningGap)
	}
	if secret.Occurrences > 1 {
		details["occurrences"] = secret.Occurrences
	}
	return ApprovalSubject{Kind: ApprovalSubjectSecret, Title: firstNonEmpty(title, "Review detected credential"), Targets: []ApprovalTarget{{
		Kind: "secret", Label: secretEvidenceLabel(secret), Details: details,
	}}}
}

// secretEvidenceLabel combines shape and variable provenance.
func secretEvidenceLabel(secret *SecretScreen) string {
	rule := strings.TrimSpace(secret.RuleTitle)
	name := strings.TrimSpace(secret.VarName)
	container := strings.TrimSpace(secret.Container)
	if name == "" || container == "" {
		return rule
	}
	provenance := "the value of " + name + " from " + container
	if rule == "" || strings.HasPrefix(rule, "A value from ") {
		return provenance
	}
	return rule + " · " + provenance
}

func actionLabel(action ProposedAction) string {
	if strings.TrimSpace(action.Command) != "" {
		return "Run command"
	}
	if action.Tool == "" {
		return "Run action"
	}
	return "Use " + action.Tool
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
