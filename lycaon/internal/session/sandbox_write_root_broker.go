package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/sensitivepath"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/pkg/api"
)

// WriteRootCheckpointBroker resolves write-root approval plans.
type WriteRootCheckpointBroker struct {
	Checkpoints hitl.CheckpointManager
	Store       Store
	Runtime     *approvalstate.SandboxPathGrantRuntime
	// ReadRuntime holds chat read-path leases, a separate namespace from
	// write roots: a grant in one never covers the other.
	ReadRuntime *approvalstate.SandboxPathGrantRuntime
	Consequence tools.ConsequenceDeriver
	Authority   hitl.ApprovalGate
	// ApprovalsDisabled authorizes the request without a prompt.
	ApprovalsDisabled func(projectDir string) bool
	// Posture reads the ask-line for a project.
	Posture func(projectDir string) gate.Posture
	// Rule returns the effective exact write-root rule.
	Rule func(ctx context.Context, projectID, projectDir, proposedRoot string) (settings.ApprovalRule, bool)
	// Locations supplies sensitive-path matches.
	Locations *sensitivepath.Catalog
	Authz     authzledger.Recorder
}

// posture reads the ask-line for the attributed project.
func (b *WriteRootCheckpointBroker) posture(projectDir string) gate.Posture {
	if b.Posture == nil {
		return gate.DefaultPosture
	}
	return b.Posture(projectDir)
}

// Authorize implements native.SandboxWriteRootGate.
func (b *WriteRootCheckpointBroker) Authorize(ctx context.Context, in native.SandboxWriteRootAsk) (native.SandboxWriteRootResult, error) {
	var out native.SandboxWriteRootResult
	if b == nil || b.Checkpoints == nil || b.Runtime == nil {
		return out, nil
	}
	proposed := confine.NormalizeWriteRootKey(in.ProposedWriteRoot)
	if proposed == "" {
		return out, nil
	}
	// The invocation's own scratch is already inside its boundary.
	if confine.WithinSessionScratch(proposed, in.SessionScratchRoot) {
		return native.SandboxWriteRootResult{Authorized: true, ProposedWriteRoot: proposed}, nil
	}
	// Control-plane writes are denied without prompting.
	if confine.ControlPlanePathDenied(proposed, true, in.SessionScratchRoot) {
		return out, nil
	}
	subject := confine.ClassifyBlockedWrite(proposed)
	if subject.Kind == confine.WriteSubjectCredentialStore {
		// Credential stores grant only the declared file.
		proposed = subject.GrantPath
	}
	if subject.Kind == confine.WriteSubjectOrdinary {
		// A root granted here must also pass the boundary's granted lane.
		if refused, _ := confine.GrantedWriteRootRefused(proposed); refused {
			return out, nil
		}
	}
	invokingSessionID, rootSessionID := askSessionIDs(ctx, b.Store, in.SessionID, in.ParentSessionID)
	projectDir := strings.TrimSpace(in.ProjectDir)
	roots := []string{}
	if projectDir != "" {
		roots = []string{projectDir}
	}
	// Effective roots include durable and chat-scoped grants.
	writeRoots := confine.WriteRootsForBoundary(in.ProjectID, roots, nil, in.SessionScratchRoot)
	writeRoots = append(writeRoots, b.Runtime.SessionWriteRoots(rootSessionID)...)
	inRoots := confine.PathWithinWriteRoots(proposed, writeRoots)
	if subject.Kind == confine.WriteSubjectOrdinary {
		// Ordinary paths can use a covering chat grant.
		if covering := sessionOverlayCovering(b.Runtime, rootSessionID, proposed); covering != "" {
			return native.SandboxWriteRootResult{
				Authorized:        true,
				ProposedWriteRoot: covering,
			}, nil
		}
		if inRoots {
			return native.SandboxWriteRootResult{Authorized: true, ProposedWriteRoot: proposed}, nil
		}
	} else if sessionOverlayHasExact(b.Runtime, rootSessionID, proposed) {
		return native.SandboxWriteRootResult{Authorized: true, ProposedWriteRoot: proposed}, nil
	}
	var userRule *gate.UserRule
	var approvalMatches []hitl.ApprovalRuleMatch
	if b.Rule != nil {
		rule, matched := b.Rule(ctx, in.ProjectID, projectDir, proposed)
		if matched && rule.Effect == settings.ApprovalEffectDeny {
			b.recordRuleDeny(ctx, invokingSessionID, in, proposed, rule)
			return native.SandboxWriteRootResult{Denied: true, ProposedWriteRoot: proposed}, nil
		}
		if matched && rule.Effect == settings.ApprovalEffectAsk {
			approvalMatches = []hitl.ApprovalRuleMatch{approvalRuleMatch(rule)}
			userRule = &gate.UserRule{
				Category: string(settings.ApprovalCategoryWriteRoot),
				Pattern:  rule.Pattern,
				Subject:  proposed,
			}
		}
	}
	// The posture decides whether declared authority needs a prompt.
	target := &gate.FileTarget{
		Path: proposed, Mode: gate.ModeWrite,
		OutsideRoots: !inRoots,
	}
	// Protected subjects ask regardless of root or catalog coverage.
	if subject.Kind != confine.WriteSubjectOrdinary {
		target.ProtectedSubject = true
	}
	// ClassifyResolved covers sensitive descendants and symlink aliases using the file-write boundary.
	if b.Locations != nil {
		if m, ok := b.Locations.ClassifyResolved(proposed, sensitivepath.ModeWrite); ok {
			target.Sensitive = true
			target.CatalogID = m.ID
			target.CatalogTitle = m.Title
			target.ProtectedSubject = target.ProtectedSubject || m.Protected
		}
	}
	facts := gate.Facts{
		Stage: gate.StagePreSpawn,
		Ran: gate.ProducerContainment | gate.ProducerDetection | gate.ProducerConsent |
			gate.ProducerFilePath | gate.ProducerLease | gate.ProducerRule,
		File: target, UserRule: userRule,
	}
	verdict, decision := gate.Evaluate(facts, b.posture(projectDir))

	autoGrant := verdict != gate.Ask
	if b.ApprovalsDisabled != nil && b.ApprovalsDisabled(projectDir) {
		autoGrant = true
	}
	if autoGrant {
		b.Runtime.ClearDenied(invokingSessionID, proposed)
		b.Runtime.GrantSessionWriteRoot(rootSessionID, proposed)
		return native.SandboxWriteRootResult{
			Authorized:        true,
			ProposedWriteRoot: proposed,
		}, nil
	}

	return b.raiseWriteRootCard(ctx, in, invokingSessionID, rootSessionID, projectDir, proposed, subject, decision, approvalMatches)
}

// raiseWriteRootCard builds and awaits a write-root checkpoint.
func (b *WriteRootCheckpointBroker) raiseWriteRootCard(
	ctx context.Context,
	in native.SandboxWriteRootAsk,
	invokingSessionID, rootSessionID, projectDir, proposed string,
	subject confine.WriteSubject,
	decision *gate.Decision,
	approvalMatches []hitl.ApprovalRuleMatch,
) (native.SandboxWriteRootResult, error) {
	resolved, err := awaitSandboxAsk(ctx, sandboxAskRequest{
		Gate: b.Runtime, Checkpoints: b.Checkpoints, Authz: b.Authz,
		InvokingSessionID: invokingSessionID, Key: proposed, ToolCallID: in.ToolCallID,
		ProjectID: in.ProjectID, Tool: "write_root",
		AskFamily: authzledger.AskFamilyWriteRoot, CheckpointLabel: "write-root",
		BuildCard: func() (sandboxAskCard, error) {
			return b.buildWriteRootCard(in, invokingSessionID, rootSessionID, projectDir, proposed, subject, decision, approvalMatches)
		},
	})
	if err != nil || !resolved.Answered {
		return native.SandboxWriteRootResult{}, err
	}
	return native.SandboxWriteRootResult{
		Raised:            resolved.Raised,
		Authorized:        resolved.Authorized,
		Denied:            resolved.Denied,
		ProposedWriteRoot: proposed,
		UserGuidance:      resolved.UserGuidance,
	}, nil
}

type writeRootCardCopy struct {
	targetKind, subjectTitle, impactLine, ifWrongLine, allowLine string
}

func writeRootCardCopyFor(subject confine.WriteSubject, proposed string, protectedWithin []string) writeRootCardCopy {
	out := writeRootCardCopy{
		targetKind:   "write_root",
		subjectTitle: "Allow write access",
		impactLine:   "Allow future commands in this chat to write within the listed root.",
		ifWrongLine:  "Programs can create or modify files anywhere under this root.",
		allowLine:    "writes within " + proposed,
	}
	if subject.Kind == confine.WriteSubjectOrdinary && len(protectedWithin) > 0 {
		// Catalogued credential paths stay denied inside the root; say so.
		out.ifWrongLine = "Programs can create or modify files anywhere under this root, " +
			"except credential files inside, which stay protected and are not part of this grant."
	}
	switch subject.Kind {
	case confine.WriteSubjectCredentialStore:
		// Credential asks name the file, not a write-root directory.
		out.targetKind = "credential_file"
		out.subjectTitle = "Allow this credential file to be rewritten"
		out.impactLine = "Allow this chat to rewrite this one credential file."
		out.ifWrongLine = "The stored login in this file can be replaced. Nothing else in the directory is granted."
		out.allowLine = "rewriting " + proposed
	case confine.WriteSubjectKeyMaterial:
		out.targetKind = "key_material"
		out.subjectTitle = "Allow writes to key material"
		out.impactLine = "Allow this chat to write to this key-material path for the rest of the chat."
		out.ifWrongLine = "Keys here authenticate as you. A program that writes them can replace or add credentials that act as this machine."
		out.allowLine = "writes to " + proposed
	case confine.WriteSubjectOrdinary:
	}
	return out
}

func (b *WriteRootCheckpointBroker) buildWriteRootCard(
	in native.SandboxWriteRootAsk,
	invokingSessionID, rootSessionID, projectDir, proposed string,
	subject confine.WriteSubject,
	decision *gate.Decision,
	approvalMatches []hitl.ApprovalRuleMatch,
) (sandboxAskCard, error) {
	summary := sandboxAskCommandSummary(in.Command)
	toolName := strings.TrimSpace(in.ToolName)
	var band api.ConsequenceBand
	var code api.ConsequenceCode
	if b.Consequence != nil {
		band, code = b.Consequence.WriteRoot(proposed)
	}
	grantAction := hitl.ProposedAction{
		Tool: "write_root", Args: map[string]any{"proposed_write_root": proposed},
		ProjectID: in.ProjectID, ProjectDir: projectDir, SessionID: invokingSessionID, RootSessionID: rootSessionID,
		Contained: hitl.ContainedForAction(hitl.ActionConfineInputs{
			ProjectID:         in.ProjectID,
			Roots:             projectRoots(projectDir),
			OverlayWriteRoots: b.Runtime.SessionWriteRoots(rootSessionID),
		}),
	}
	var grantOffers []hitl.ApprovalGrantOffer
	if b.Authority != nil && subject.Kind == confine.WriteSubjectOrdinary {
		grantOffers = b.Authority.GrantOffers(grantAction, &hitl.ApprovalResult{Decision: decision})
	}
	protectedWithin := confine.ProtectedPathsWithin(proposed)
	card := writeRootCardCopyFor(subject, proposed, protectedWithin)
	details := map[string]any{
		"blocked_path": proposed,
	}
	if len(protectedWithin) > 0 {
		details["protected_paths_within"] = protectedWithin
	}
	target := hitl.ApprovalTarget{Kind: card.targetKind, Label: proposed, Details: details}
	chatGrant := writeRootChatGrant(grantAction, proposed)
	chatDelta := hitl.ApprovalAuthorityDelta{
		Kind: hitl.AuthorityWriteRootChat, Grant: &chatGrant,
		ChatSessionID: rootSessionID, WriteRoots: []string{proposed},
	}
	if len(grantOffers) == 0 {
		grantOffers = []hitl.ApprovalGrantOffer{{
			ID: chatGrant.ID, Rung: hitl.ApprovalRungChat, Scope: hitl.ApprovalGrantScopeChat,
			Title: chatGrant.Title, Coverage: chatGrant.Coverage, ExpiresWhen: chatGrant.ExpiresWhen,
			ReaskWhen: chatGrant.ReaskWhen, Subject: gate.ReusePredicate, Grant: chatGrant,
			Authority: []hitl.ApprovalAuthorityDelta{chatDelta},
		}}
	}
	options := make([]hitl.ApprovalOption, 0, len(grantOffers))
	for _, offer := range grantOffers {
		if len(offer.Authority) > 0 {
			options = append(options, hitl.GrantOption(offer))
			continue
		}
		// Every rung continues the held write through the chat's runtime root;
		// only the Day rung bounds that root to its 24 hours.
		continuing := chatDelta
		continuing.TTLSeconds = offer.TTLSeconds
		options = append(options, hitl.ContinuingLeaseOption(offer, continuing))
	}
	options = append(options, hitl.QuietOptions(grantAction, decision, nil, func(key string) bool {
		if b.Authority == nil {
			return false
		}
		_, live := b.Authority.AskQuietLive(grantAction.ChatSession(), key)
		return live
	})...)
	primaryGate, cited, reasons := hitl.PresentDecision(decision)
	plan, err := hitl.NewApprovalPlan(grantAction, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectWriteRootSet, Title: card.subjectTitle, Targets: []hitl.ApprovalTarget{target},
	}, hitl.ApprovalPresentation{
		Action: "Sandbox write access", Tool: toolName, Command: summary,
		Impact:          card.impactLine,
		Who:             "this chat and its workers",
		IfWrong:         card.ifWrongLine,
		AllowLine:       card.allowLine,
		Gate:            primaryGate,
		Cited:           cited,
		ApprovalRules:   approvalMatches,
		ConsequenceBand: string(band), ConsequenceCode: string(code),
	}, reasons, options, hitl.FaceContext{})
	if err != nil {
		return sandboxAskCard{}, tools.ApprovalPlanInvalid()
	}
	return sandboxAskCard{
		Action: grantAction, Title: card.subjectTitle, Plan: plan,
		Decision: decision, ApprovalMatches: approvalMatches,
	}, nil
}

func approvalRuleMatch(rule settings.ApprovalRule) hitl.ApprovalRuleMatch {
	return hitl.ApprovalRuleMatch{
		Category: string(rule.Category), Pattern: rule.Pattern, Effect: string(rule.Effect),
		UnitID: rule.Source.UnitID, PackID: rule.Source.PackID, Scope: string(rule.Source.Scope),
	}
}

func (b *WriteRootCheckpointBroker) recordRuleDeny(
	ctx context.Context,
	sessionID string,
	in native.SandboxWriteRootAsk,
	proposed string,
	rule settings.ApprovalRule,
) {
	if b == nil || b.Authz == nil {
		return
	}
	match := approvalRuleMatch(rule)
	b.Authz.AppendToolDenied(ctx, authzledger.ToolDeniedRecord{
		SessionID: sessionID, Tool: "write_root", ProjectDir: in.ProjectDir,
		Args:        map[string]any{"proposed_write_root": proposed},
		BlockReason: "APPROVAL_RULE_DENIED", RejectCode: "APPROVAL_RULE_DENIED",
		ApprovalRules: []authzledger.ApprovalRuleCitation{{
			Category: match.Category, Pattern: match.Pattern, Effect: match.Effect,
			UnitID: match.UnitID, PackID: match.PackID, Scope: match.Scope,
		}},
	})
}

func writeRootChatGrant(action hitl.ProposedAction, root string) hitl.ApprovalGrant {
	root = confine.NormalizeWriteRootKey(root)
	raw := strings.Join([]string{
		string(hitl.ApprovalGrantScopeChat), hitl.ApprovalGrantCategoryWriteRoot,
		root, action.ChatSession(),
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return hitl.ApprovalGrant{
		ID: "grant_" + hex.EncodeToString(sum[:8]), Scope: hitl.ApprovalGrantScopeChat,
		Predicate:     hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryWriteRoot, Pattern: root},
		ChatSessionID: action.ChatSession(), ProjectID: action.ProjectID, ProjectDir: action.ProjectDir,
		Title: hitl.TitleAllowForThisChat, Coverage: "writes within `" + root + "`",
		GrantedAt: time.Now().UTC(), ExpiresWhen: hitl.ExpiresWhenChatDeleted,
		ReaskWhen: "a different write root is needed", Source: "checkpoint",
	}
}

func resolvedUserGuidance(final *hitl.CheckpointResponse) string {
	if final == nil || final.Result == nil || final.Status != hitl.DecisionStatusRejected {
		return ""
	}
	return strings.TrimSpace(final.Result.Comments)
}

// SessionWriteRoots returns chat-scoped write grants.
func (b *WriteRootCheckpointBroker) SessionWriteRoots(ctx context.Context, sessionID, parentSessionID string) []string {
	if b == nil || b.Runtime == nil {
		return nil
	}
	_, rootSessionID := askSessionIDs(ctx, b.Store, sessionID, parentSessionID)
	return b.Runtime.SessionWriteRoots(rootSessionID)
}

func sessionOverlayCovering(rt *approvalstate.SandboxPathGrantRuntime, chatSessionID, blocked string) string {
	if rt == nil {
		return ""
	}
	for _, root := range rt.SessionWriteRoots(chatSessionID) {
		if confine.PathWithinWriteRoots(blocked, []string{root}) {
			return root
		}
	}
	return ""
}

// sessionOverlayHasExact requires an exact grant for protected paths.
func sessionOverlayHasExact(rt *approvalstate.SandboxPathGrantRuntime, chatSessionID, path string) bool {
	if rt == nil {
		return false
	}
	key := confine.NormalizeWriteRootKey(path)
	for _, root := range rt.SessionWriteRoots(chatSessionID) {
		if confine.NormalizeWriteRootKey(root) == key {
			return true
		}
	}
	return false
}

// projectRoots is the write-jail root set a write-root card is reviewed under.
func projectRoots(projectDir string) []string {
	if strings.TrimSpace(projectDir) == "" {
		return nil
	}
	return []string{projectDir}
}
