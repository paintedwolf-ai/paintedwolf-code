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
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/pkg/api"
)

// AuthorizeRead grants protected reads and silently denies control-plane reads.
func (b *WriteRootCheckpointBroker) AuthorizeRead(ctx context.Context, in native.SandboxReadPathAsk) (native.SandboxReadPathResult, error) {
	var out native.SandboxReadPathResult
	if b == nil || b.Checkpoints == nil || b.ReadRuntime == nil {
		return out, nil
	}
	proposed := confine.NormalizeWriteRootKey(in.ProposedReadPath)
	if proposed == "" {
		return out, nil
	}
	// Control-plane reads are denied without prompting.
	if confine.ControlPlaneReadDenied(proposed) {
		return native.SandboxReadPathResult{Denied: true, ProposedReadPath: proposed}, nil
	}
	if !b.readPathNeedsApproval(proposed, in.ReadDenyPaths) {
		// Nothing denies this read; the declaration needs no grant.
		return native.SandboxReadPathResult{Authorized: true, ProposedReadPath: proposed}, nil
	}
	invokingSessionID, rootSessionID := askSessionIDs(ctx, b.Store, in.SessionID, in.ParentSessionID)
	if sessionOverlayHasExact(b.ReadRuntime, rootSessionID, proposed) {
		return native.SandboxReadPathResult{Authorized: true, ProposedReadPath: proposed}, nil
	}
	projectDir := strings.TrimSpace(in.ProjectDir)
	target := &gate.FileTarget{
		Path: proposed, Mode: gate.ModeRead,
		OutsideRoots: true, ProtectedSubject: true,
	}
	if b.Locations != nil {
		if m, ok := b.Locations.ClassifyResolved(proposed, sensitivepath.ModeRead); ok {
			target.Sensitive = true
			target.CatalogID = m.ID
			target.CatalogTitle = m.Title
		}
	}
	facts := gate.Facts{
		Stage: gate.StagePreSpawn,
		Ran: gate.ProducerContainment | gate.ProducerDetection | gate.ProducerConsent |
			gate.ProducerFilePath | gate.ProducerLease | gate.ProducerRule,
		File: target,
	}
	verdict, decision := gate.Evaluate(facts, b.posture(projectDir))

	autoGrant := verdict != gate.Ask
	if b.ApprovalsDisabled != nil && b.ApprovalsDisabled(projectDir) {
		autoGrant = true
	}
	if autoGrant {
		b.ReadRuntime.ClearDenied(invokingSessionID, proposed)
		b.ReadRuntime.GrantSessionWriteRoot(rootSessionID, proposed)
		return native.SandboxReadPathResult{Authorized: true, ProposedReadPath: proposed}, nil
	}
	return b.raiseReadPathCard(ctx, in, invokingSessionID, rootSessionID, projectDir, proposed, decision)
}

func (b *WriteRootCheckpointBroker) readPathNeedsApproval(path string, deniedPaths []string) bool {
	if confine.KeyMaterialPath(path) {
		return true
	}
	for _, denied := range deniedPaths {
		if confine.PathAtOrUnder(path, denied) || confine.PathAtOrUnder(denied, path) {
			return true
		}
	}
	if b.Locations != nil {
		if match, ok := b.Locations.ClassifyResolved(path, sensitivepath.ModeRead); ok && match.Protected {
			return true
		}
	}
	return false
}

// SessionReadPaths returns chat-scoped read grants.
func (b *WriteRootCheckpointBroker) SessionReadPaths(ctx context.Context, sessionID, parentSessionID string) []string {
	if b == nil || b.ReadRuntime == nil {
		return nil
	}
	_, rootSessionID := askSessionIDs(ctx, b.Store, sessionID, parentSessionID)
	return b.ReadRuntime.SessionWriteRoots(rootSessionID)
}

// raiseReadPathCard builds and awaits a read-path checkpoint.
func (b *WriteRootCheckpointBroker) raiseReadPathCard(
	ctx context.Context,
	in native.SandboxReadPathAsk,
	invokingSessionID, rootSessionID, projectDir, proposed string,
	decision *gate.Decision,
) (native.SandboxReadPathResult, error) {
	resolved, err := awaitSandboxAsk(ctx, sandboxAskRequest{
		Gate: b.ReadRuntime, Checkpoints: b.Checkpoints, Authz: b.Authz,
		InvokingSessionID: invokingSessionID, Key: proposed, ToolCallID: in.ToolCallID,
		ProjectID: in.ProjectID, Tool: "read_path",
		AskFamily: authzledger.AskFamilyReadPath, CheckpointLabel: "read-path",
		BuildCard: func() (sandboxAskCard, error) {
			return b.buildReadPathCard(in, invokingSessionID, rootSessionID, projectDir, proposed, decision)
		},
	})
	if err != nil || !resolved.Answered {
		return native.SandboxReadPathResult{}, err
	}
	return native.SandboxReadPathResult{
		Raised: resolved.Raised, Authorized: resolved.Authorized, Denied: resolved.Denied,
		ProposedReadPath: proposed, UserGuidance: resolved.UserGuidance,
	}, nil
}

func (b *WriteRootCheckpointBroker) buildReadPathCard(
	in native.SandboxReadPathAsk,
	invokingSessionID, rootSessionID, projectDir, proposed string,
	decision *gate.Decision,
) (sandboxAskCard, error) {
	summary := sandboxAskCommandSummary(in.Command)
	grantAction := hitl.ProposedAction{
		Tool: "read_path", Args: map[string]any{"proposed_read_path": proposed},
		ProjectID: in.ProjectID, ProjectDir: projectDir, SessionID: invokingSessionID, RootSessionID: rootSessionID,
		Contained: hitl.ContainedForAction(hitl.ActionConfineInputs{
			ProjectID: in.ProjectID,
			Roots:     projectRoots(projectDir),
		}),
	}
	chatGrant := readPathChatGrant(grantAction, proposed)
	chatDelta := hitl.ApprovalAuthorityDelta{
		Kind: hitl.AuthorityReadPathChat, Grant: &chatGrant,
		ChatSessionID: rootSessionID, ReadPaths: []string{proposed},
	}
	options := []hitl.ApprovalOption{hitl.GrantOption(hitl.ApprovalGrantOffer{
		ID: chatGrant.ID, Rung: hitl.ApprovalRungChat, Scope: hitl.ApprovalGrantScopeChat,
		Title: chatGrant.Title, Coverage: chatGrant.Coverage, ExpiresWhen: chatGrant.ExpiresWhen,
		ReaskWhen: chatGrant.ReaskWhen, Subject: gate.ReusePredicate, Grant: chatGrant,
		Authority: []hitl.ApprovalAuthorityDelta{chatDelta},
	})}
	subjectTitle := "Allow reading a protected path"
	target := hitl.ApprovalTarget{Kind: "read_path", Label: proposed}
	var band api.ConsequenceBand
	var code api.ConsequenceCode
	if b.Consequence != nil {
		band, code = b.Consequence.WriteRoot(proposed)
	}
	primaryGate, cited, reasons := hitl.PresentDecision(decision)
	plan, err := hitl.NewApprovalPlan(grantAction, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectReadPathSet, Title: subjectTitle, Targets: []hitl.ApprovalTarget{target},
	}, hitl.ApprovalPresentation{
		Action: "Sandbox read access", Tool: strings.TrimSpace(in.ToolName), Command: summary,
		Impact:          "Allow this chat to read this protected path.",
		Who:             "this chat and its workers",
		IfWrong:         "Anything that reads this path can copy its contents.",
		AllowLine:       "reading " + proposed,
		Gate:            primaryGate,
		Cited:           cited,
		ConsequenceBand: string(band), ConsequenceCode: string(code),
	}, reasons, options, hitl.FaceContext{})
	if err != nil {
		return sandboxAskCard{}, tools.ApprovalPlanInvalid()
	}
	return sandboxAskCard{Action: grantAction, Title: subjectTitle, Plan: plan, Decision: decision}, nil
}

func readPathChatGrant(action hitl.ProposedAction, path string) hitl.ApprovalGrant {
	path = confine.NormalizeWriteRootKey(path)
	raw := strings.Join([]string{
		string(hitl.ApprovalGrantScopeChat), hitl.ApprovalGrantCategoryReadPath,
		path, action.ChatSession(),
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return hitl.ApprovalGrant{
		ID: "grant_" + hex.EncodeToString(sum[:8]), Scope: hitl.ApprovalGrantScopeChat,
		Predicate:     hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryReadPath, Pattern: path},
		ChatSessionID: action.ChatSession(), ProjectID: action.ProjectID, ProjectDir: action.ProjectDir,
		Title: hitl.TitleAllowForThisChat, Coverage: "reading `" + path + "`",
		GrantedAt: time.Now().UTC(), ExpiresWhen: hitl.ExpiresWhenChatDeleted,
		ReaskWhen: "a different protected path is needed", Source: "checkpoint",
	}
}
