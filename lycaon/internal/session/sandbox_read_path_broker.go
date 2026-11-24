package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/sensitivepath"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/pkg/api"
)

// AuthorizeRead grants protected and shared-temp reads while silently denying control-plane reads.
func (b *WriteRootCheckpointBroker) AuthorizeRead(ctx context.Context, in command.SandboxReadPathAsk) (command.SandboxReadPathResult, error) {
	if b == nil || b.Runtime == nil || !overlapsSharedTemp(in.ProposedReadPath) {
		return b.authorizeReadPath(ctx, in, awaitSandboxAsk)
	}
	_, chat := askSessionIDs(ctx, b.Store, in.SessionID, in.ParentSessionID)
	history, err := b.Runtime.SharedTempHistory(ctx, chat, b.TempHistory)
	if err != nil {
		return command.SandboxReadPathResult{}, err
	}
	end, err := history.BeginReview(ctx)
	if err != nil {
		return command.SandboxReadPathResult{}, err
	}
	defer end()
	return b.authorizeReadPath(ctx, in, func(ctx context.Context, req sandboxAskRequest) (sandboxAskResolution, error) {
		result, err := awaitSandboxAsk(ctx, req)
		if err == nil && result.HumanDeclined {
			history.Declined[sharedTempReadHistoryKey(req.Key)] = true
		}
		return result, err
	})
}

func (b *WriteRootCheckpointBroker) authorizeReadPath(ctx context.Context, in command.SandboxReadPathAsk, review writeRootReview) (command.SandboxReadPathResult, error) {
	var out command.SandboxReadPathResult
	if b == nil || b.Checkpoints == nil || b.ReadRuntime == nil {
		return out, nil
	}
	proposed := confine.NormalizeWriteRootKey(in.ProposedReadPath)
	if proposed == "" {
		return out, nil
	}
	// Control-plane reads are denied without prompting.
	if confine.ControlPlanePathDenied(proposed, false, in.SessionScratchRoot, in.SessionOutputRoot) {
		return command.SandboxReadPathResult{Denied: true, ProposedReadPath: proposed}, nil
	}
	strongerFloor := b.strongerReadFloor(proposed, in.ReadDenyPaths)
	if !strongerFloor && confine.SharedTempLocation(proposed) != "" {
		if cp := fspath.CanonicalPath(proposed); cp != "" {
			proposed = cp
		}
		if confine.ControlPlanePathDenied(proposed, false, in.SessionScratchRoot, in.SessionOutputRoot) {
			return command.SandboxReadPathResult{Denied: true, ProposedReadPath: proposed}, nil
		}
		strongerFloor = b.strongerReadFloor(proposed, in.ReadDenyPaths)
	}
	shared := !strongerFloor && overlapsSharedTemp(proposed) && !b.explicitTempReadAllowed(in, proposed)
	if confine.PathAtOrUnder(proposed, in.SessionScratchRoot) || (!strongerFloor && !shared) {
		// Nothing denies this read; the declaration needs no grant.
		return command.SandboxReadPathResult{Authorized: true, ProposedReadPath: proposed}, nil
	}
	invokingSessionID, rootSessionID := askSessionIDs(ctx, b.Store, in.SessionID, in.ParentSessionID)
	if strongerFloor {
		if sessionOverlayHasExact(b.ReadRuntime, rootSessionID, proposed) {
			return command.SandboxReadPathResult{Authorized: true, ProposedReadPath: proposed}, nil
		}
	} else if b.readGrantCovers(rootSessionID, in.ProjectID, proposed) {
		return command.SandboxReadPathResult{Authorized: true, ProposedReadPath: proposed}, nil
	}
	projectDir := strings.TrimSpace(in.ProjectDir)
	target := &gate.FileTarget{
		Path: proposed, Mode: gate.ModeRead,
		OutsideRoots: true, ProtectedSubject: strongerFloor,
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
	if autoGrant && !shared {
		b.ReadRuntime.GrantSessionWriteRoot(rootSessionID, proposed)
		return command.SandboxReadPathResult{Authorized: true, ProposedReadPath: proposed}, nil
	}
	return b.raiseReadPathCard(ctx, review, in, invokingSessionID, rootSessionID, projectDir, proposed, strongerFloor, decision)
}

func (b *WriteRootCheckpointBroker) strongerReadFloor(path string, deniedPaths []string) bool {
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

func (b *WriteRootCheckpointBroker) explicitTempReadAllowed(in command.SandboxReadPathAsk, path string) bool {
	root, err := confine.NativeReadRootForPath(
		in.ProjectID, projectRoots(in.ProjectDir), nil, nil, nil,
		in.SessionScratchRoot, in.SessionOutputRoot, path,
	)
	return err == nil && root != ""
}

func (b *WriteRootCheckpointBroker) readGrantCovers(rootSessionID, projectID, path string) bool {
	if readOverlayCovers(b.ReadRuntime, rootSessionID, path) {
		return true
	}
	if b != nil && b.GrantedPaths != nil {
		if _, ok := b.GrantedPaths.Covers(rootSessionID, projectID, path, grantedpath.ModeRead); ok {
			return true
		}
	}
	return false
}

func readOverlayCovers(rt *approvalstate.SandboxPathGrantRuntime, chatSessionID, path string) bool {
	if rt == nil {
		return false
	}
	canon := fspath.CanonicalPath(path)
	if canon == "" {
		canon = confine.NormalizeWriteRootKey(path)
	}
	for _, entry := range rt.SessionWriteRoots(chatSessionID) {
		resolved := fspath.CanonicalPath(entry)
		if resolved == "" {
			resolved = confine.NormalizeWriteRootKey(entry)
		}
		if confine.PathEqual(canon, resolved) {
			return true
		}
		if info, err := os.Lstat(resolved); err == nil && info.IsDir() && confine.PathAtOrUnder(canon, resolved) {
			return true
		}
	}
	return false
}

// SessionReadPaths returns chat-scoped read grants.
func (b *WriteRootCheckpointBroker) SessionReadPaths(ctx context.Context, sessionID, parentSessionID string) []string {
	if b == nil {
		return nil
	}
	_, rootSessionID := askSessionIDs(ctx, b.Store, sessionID, parentSessionID)
	var out []string
	if b.ReadRuntime != nil {
		out = append(out, b.ReadRuntime.SessionWriteRoots(rootSessionID)...)
	}
	if b.GrantedPaths != nil {
		for _, g := range b.GrantedPaths.List(rootSessionID) {
			if g.Mode.PermitsRead() {
				out = append(out, g.Path)
			}
		}
	}
	return out
}

// raiseReadPathCard builds and awaits a read-path checkpoint.
func (b *WriteRootCheckpointBroker) raiseReadPathCard(
	ctx context.Context,
	review writeRootReview,
	in command.SandboxReadPathAsk,
	invokingSessionID, rootSessionID, projectDir, proposed string,
	strongerFloor bool,
	decision *gate.Decision,
) (command.SandboxReadPathResult, error) {
	resolved, err := review(ctx, sandboxAskRequest{
		Gate: b.ReadRuntime, Checkpoints: b.Checkpoints, Authz: b.Authz,
		InvokingSessionID: invokingSessionID, Key: proposed, ToolCallID: in.ToolCallID,
		ProjectID: in.ProjectID, Tool: "read_path",
		AskFamily: authzledger.AskFamilyReadPath, CheckpointLabel: "read-path",
		BuildCard: func() (sandboxAskCard, error) {
			return b.buildReadPathCard(in, invokingSessionID, rootSessionID, projectDir, proposed, strongerFloor, decision)
		},
	})
	if err != nil || !resolved.Answered {
		return command.SandboxReadPathResult{}, err
	}
	return command.SandboxReadPathResult{
		Raised: resolved.Raised, Authorized: resolved.Authorized, Denied: resolved.Denied,
		ProposedReadPath: proposed, UserGuidance: resolved.UserGuidance,
	}, nil
}

func (b *WriteRootCheckpointBroker) buildReadPathCard(
	in command.SandboxReadPathAsk,
	invokingSessionID, rootSessionID, projectDir, proposed string,
	strongerFloor bool,
	decision *gate.Decision,
) (sandboxAskCard, error) {
	summary := sandboxAskCommandSummary(in.Command)
	grantAction := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "read_path",
			Args: map[string]any{"proposed_read_path": proposed},
		},
		Scope: hitl.ActionScope{
			ProjectID:     in.ProjectID,
			ProjectDir:    projectDir,
			SessionID:     invokingSessionID,
			RootSessionID: rootSessionID,
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.ContainedForAction(hitl.ActionConfineInputs{
				ProjectID: in.ProjectID,
				Roots:     projectRoots(projectDir),
			}),
		},
	}
	sharedTemp := !strongerFloor && confine.SharedTempLocation(proposed) != ""
	info, statErr := os.Lstat(proposed)
	isDir := statErr == nil && info.IsDir()

	chatGrant := readPathChatGrant(grantAction, proposed)
	chatDelta := hitl.ApprovalAuthorityDelta{
		Kind: hitl.AuthorityReadPathChat, Grant: &chatGrant,
		ChatSessionID: rootSessionID, ReadPaths: []string{proposed},
	}
	authority := []hitl.ApprovalAuthorityDelta{chatDelta}
	if sharedTemp {
		if isDir {
			chatGrant.Coverage = "reading and listing `" + proposed + "`"
			if isSharedTempRoot(proposed) {
				chatGrant.Coverage += " — other applications' temporary files in " + proposed + " are included"
			}
		} else {
			chatGrant.Coverage = "reading `" + proposed + "`"
		}
		pathAccess := hitl.GrantedPathDelta{Path: proposed, Write: false, Tree: isDir}
		pathGrant := chatGrant
		pathGrant.ID = chatGrant.ID + "_p"
		pathGrant.Predicate = hitl.ApprovalGrantPredicate{Category: "path", Pattern: proposed}
		pathGrant.GrantedPath = &pathAccess
		authority = append(authority,
			hitl.ApprovalAuthorityDelta{
				Kind: hitl.AuthorityGrantedPath, Grant: &pathGrant,
				ChatSessionID: rootSessionID, GrantedPath: &pathAccess,
			},
			hitl.ApprovalAuthorityDelta{
				Kind: hitl.AuthorityGenericGrant, Grant: &pathGrant,
			},
		)
	}
	options := []hitl.ApprovalOption{hitl.GrantOption(hitl.ApprovalGrantOffer{
		ID: chatGrant.ID, Rung: hitl.ApprovalRungChat, Scope: hitl.ApprovalGrantScopeChat,
		Title: chatGrant.Title, Coverage: chatGrant.Coverage, ExpiresWhen: chatGrant.ExpiresWhen,
		ReaskWhen: chatGrant.ReaskWhen, Subject: gate.ReusePredicate, Grant: chatGrant,
		Authority: authority,
	})}

	subjectTitle := "Allow reading a protected path"
	targetLabel := proposed
	actionLabel := "Sandbox read access"
	impactLine := "Allow this chat to read this protected path."
	ifWrongLine := "Anything that reads this path can copy its contents."
	allowLine := "reading " + proposed
	details := map[string]any{"blocked_path": proposed}

	if sharedTemp {
		actionLabel = "System temp access"
		details["access"] = "read"
		details["shared_temp"] = true
		details["tree"] = isDir
		if isDir {
			subjectTitle = "Allow reading and listing " + proposed
			targetLabel = "Read and list " + proposed
			if isSharedTempRoot(proposed) {
				impactLine = "Other applications' temporary files in " + proposed + " are included."
			} else {
				impactLine = "Allow this chat to read files and list entries in " + proposed + "."
			}
			allowLine = "reading and listing " + proposed + " for this chat"
			options = append(options, hitl.SharedTempExchangeFolderOption(grantAction, proposed, isSharedTempRoot(proposed)))
		} else {
			subjectTitle = "Allow reading " + proposed
			targetLabel = "Read " + proposed
			impactLine = "Allow this chat to read this file in system temp."
			allowLine = "reading " + proposed + " for this chat"
			parentDir := filepath.Dir(proposed)
			if confine.SharedTempLocation(parentDir) != "" {
				if pInfo, pErr := os.Lstat(parentDir); pErr == nil && pInfo.IsDir() {
					options = append(options, sharedTempDirectoryReadOption(grantAction, parentDir, isSharedTempRoot(parentDir)))
					options = append(options, hitl.SharedTempExchangeFolderOption(grantAction, parentDir, isSharedTempRoot(parentDir)))
				}
			}
		}
	}

	target := hitl.ApprovalTarget{Kind: "read_path", Label: targetLabel, Details: details}
	var band api.ConsequenceBand
	var code api.ConsequenceCode
	if b.Consequence != nil {
		band, code = b.Consequence.WriteRoot(proposed)
	}
	primaryGate, cited, reasons := hitl.PresentDecision(decision)
	plan, err := hitl.NewApprovalPlan(grantAction, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectReadPathSet, Title: subjectTitle, Targets: []hitl.ApprovalTarget{target},
	}, hitl.ApprovalPresentation{
		Action: actionLabel, Tool: strings.TrimSpace(in.ToolName), Command: summary,
		Impact:          impactLine,
		Who:             "this chat and its workers",
		IfWrong:         ifWrongLine,
		AllowLine:       allowLine,
		Gate:            primaryGate,
		Cited:           cited,
		ConsequenceBand: string(band), ConsequenceCode: string(code),
	}, reasons, options, hitl.FaceContext{})
	if err != nil {
		return sandboxAskCard{}, toolrejection.ApprovalPlanInvalid()
	}
	return sandboxAskCard{Action: grantAction, Title: subjectTitle, Plan: plan, Decision: decision}, nil
}

func sharedTempDirectoryReadOption(action hitl.ProposedAction, dir string, isTopRoot bool) hitl.ApprovalOption {
	chat := action.Scope.ChatSession()
	coverage := "reading and listing `" + dir + "`"
	if isTopRoot {
		coverage += " — other applications' temporary files in " + dir + " are included"
	}
	access := hitl.GrantedPathDelta{Path: dir, Write: false, Tree: true}
	sum := sha256.Sum256([]byte(strings.Join([]string{"shared_temp_dir_read", dir, chat}, "\x00")))
	id := "grant_" + hex.EncodeToString(sum[:8])
	now := time.Now().UTC()
	grant := hitl.ApprovalGrant{
		ID: id, Scope: hitl.ApprovalGrantScopeChat,
		Predicate:     hitl.ApprovalGrantPredicate{Category: "path", Pattern: dir},
		ChatSessionID: chat, ProjectID: action.Scope.ProjectID, ProjectDir: action.Scope.ProjectDir,
		Title: "Read and list " + dir + " for this chat", Coverage: coverage,
		GrantedAt: now, ExpiresWhen: hitl.ExpiresWhenChatDeleted,
		ReaskWhen: hitl.ReaskWhenOutsideFolder, Source: "checkpoint", GrantedPath: &access,
	}
	readGrant := grant
	readGrant.ID = id + "_r"
	readGrant.Predicate = hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryReadPath, Pattern: dir}
	return hitl.GrantOption(hitl.ApprovalGrantOffer{
		ID: id, Group: hitl.GroupAlsoAllow, DirectoryScope: dir, Rung: hitl.ApprovalRungChat, Scope: hitl.ApprovalGrantScopeChat,
		Title: grant.Title, Coverage: coverage, ExpiresWhen: hitl.ExpiresWhenChatDeleted,
		ReaskWhen: hitl.ReaskWhenOutsideFolder, Subject: gate.ReusePredicate, Grant: grant,
		Authority: []hitl.ApprovalAuthorityDelta{
			{Kind: hitl.AuthorityReadPathChat, Grant: &readGrant, ChatSessionID: chat, ReadPaths: []string{dir}},
			{Kind: hitl.AuthorityGrantedPath, Grant: &grant, ChatSessionID: chat, GrantedPath: &access},
			{Kind: hitl.AuthorityGenericGrant, Grant: &grant},
		},
	})
}

func readPathChatGrant(action hitl.ProposedAction, path string) hitl.ApprovalGrant {
	path = confine.NormalizeWriteRootKey(path)
	raw := strings.Join([]string{
		string(hitl.ApprovalGrantScopeChat), hitl.ApprovalGrantCategoryReadPath,
		path, action.Scope.ChatSession(),
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return hitl.ApprovalGrant{
		ID: "grant_" + hex.EncodeToString(sum[:8]), Scope: hitl.ApprovalGrantScopeChat,
		Predicate:     hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryReadPath, Pattern: path},
		ChatSessionID: action.Scope.ChatSession(), ProjectID: action.Scope.ProjectID, ProjectDir: action.Scope.ProjectDir,
		Title: hitl.TitleAllowForThisChat, Coverage: "reading `" + path + "`",
		GrantedAt: time.Now().UTC(), ExpiresWhen: hitl.ExpiresWhenChatDeleted,
		ReaskWhen: "a different protected path is needed", Source: "checkpoint",
	}
}
