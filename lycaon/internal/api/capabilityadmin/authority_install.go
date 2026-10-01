package capabilityadmin

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
)

// InstallApprovalOption rolls back applied deltas if installation fails.
func (s *Handler) InstallApprovalOption(ctx context.Context, checkpointID string, option hitl.ApprovalOption) (func(), error) {
	s.authorityMu.Lock()
	defer s.authorityMu.Unlock()
	rollbacks := make([]func(), 0, len(option.Authority))
	rollbackAll := func() {
		for i := len(rollbacks) - 1; i >= 0; i-- {
			rollbacks[i]()
		}
	}
	for _, delta := range option.Authority {
		if delta.Grant != nil {
			if err := hitl.ValidateElevatedEffects(delta.Grant.ElevatedEffects); err != nil {
				rollbackAll()
				return nil, err
			}
		}
		if delta.AskQuiet != nil {
			if err := hitl.ValidateElevatedEffects(delta.AskQuiet.ElevatedEffects); err != nil {
				rollbackAll()
				return nil, err
			}
		}
		rollback, err := s.installApprovalDelta(ctx, checkpointID, delta)
		if err != nil {
			rollbackAll()
			return nil, err
		}
		if rollback != nil {
			rollbacks = append(rollbacks, rollback)
		}
	}
	return rollbackAll, nil
}

func (s *Handler) installApprovalDelta(ctx context.Context, checkpointID string, delta hitl.ApprovalAuthorityDelta) (func(), error) {
	switch delta.Kind {
	case hitl.AuthorityCurrentAction:
		return nil, nil
	case hitl.AuthoritySocketChat,
		hitl.AuthorityDirectIPChat,
		hitl.AuthorityWriteRootChat,
		hitl.AuthorityReadPathChat,
		hitl.AuthorityLocalListenChat,
		hitl.AuthorityLoopbackConnectChat:
		return s.installChatApprovalDelta(checkpointID, delta)
	case hitl.AuthorityGenericGrant:
		if delta.Grant == nil {
			return nil, fmt.Errorf("generic approval grant is missing")
		}
		grant := *delta.Grant
		grant.OwnerOperationID = checkpointID
		created, err := s.Gate.ApplyGrant(grant)
		if err != nil {
			return nil, err
		}
		if !created {
			return nil, nil
		}
		id := delta.Grant.ID
		return func() { _, _ = s.Gate.RevokeGrantInstalledBy(id, checkpointID) }, nil
	case hitl.AuthoritySocketPermit:
		if s.Sockets == nil {
			return nil, fmt.Errorf("socket approval authority is not configured")
		}
		requested := make([]confine.SocketGrant, 0, len(delta.Sockets))
		for _, target := range delta.Sockets {
			requested = append(requested, confine.SocketGrant{ApprovedPath: target.ApprovedPath, ResolvedPath: target.ResolvedPath})
		}
		already := s.Sockets.AuthorizedGrants("", delta.SessionID, delta.ToolCallID, delta.ActionDigest, requested)
		covered := make(map[string]struct{}, len(already))
		for _, grant := range already {
			covered[grant.ApprovedPath+"\x00"+grant.ResolvedPath] = struct{}{}
		}
		created := make([]confine.SocketGrant, 0, len(requested))
		for _, grant := range requested {
			if _, ok := covered[grant.ApprovedPath+"\x00"+grant.ResolvedPath]; ok {
				continue
			}
			s.Sockets.IssuePermit(delta.SessionID, delta.ToolCallID, delta.ActionDigest, grant)
			created = append(created, grant)
		}
		return func() {
			for _, grant := range created {
				s.Sockets.RevokePermit(delta.SessionID, delta.ToolCallID, delta.ActionDigest, grant)
			}
		}, nil
	case hitl.AuthorityDirectIPPermit:
		if s.DirectIP == nil || delta.DirectIPLease == nil {
			return nil, fmt.Errorf("direct-IP approval authority is not configured")
		}
		lease := *delta.DirectIPLease
		if s.DirectIP.Authorized(delta.SessionID, delta.ToolCallID, lease.ActionDigest) {
			return nil, nil
		}
		s.DirectIP.IssuePermit(delta.SessionID, delta.ToolCallID, lease.ActionDigest, lease.RequestDigest, lease.ConfinementDigest)
		return func() { s.DirectIP.RevokePermit(delta.SessionID, delta.ToolCallID, lease.ActionDigest) }, nil
	case hitl.AuthorityGrantedPath:
		if s.GrantedPaths == nil || delta.Grant == nil || delta.GrantedPath == nil {
			return nil, fmt.Errorf("granted-path approval authority is not configured")
		}
		mode := grantedpath.ModeRead
		if delta.GrantedPath.Write {
			mode = grantedpath.ModeWrite
		}
		created, err := s.GrantedPaths.Grant(delta.ChatSession(), grantedpath.Grant{
			ID:                 delta.Grant.ID,
			SourceCheckpointID: checkpointID,
			Path:               delta.GrantedPath.Path,
			Mode:               mode,
			Tree:               delta.GrantedPath.Tree,
			// Resolve relative TTLs when approval is applied.
			ExpiresAt: delta.Grant.ResolveExpiry(time.Now()),
		})
		if err != nil {
			return nil, err
		}
		if !created {
			return nil, nil
		}
		chatSessionID, id := delta.ChatSession(), delta.Grant.ID
		return func() { s.GrantedPaths.RevokeInstalledBy(chatSessionID, id, checkpointID) }, nil
	case hitl.AuthorityTrustDestination:
		if s.LLMService == nil || delta.TrustDestination == nil {
			return nil, fmt.Errorf("provider trust authority is not configured")
		}
		trust := *delta.TrustDestination
		changed, err := s.LLMService.TrustProviderForSecrets(ctx, trust.ProviderID, trust.DestinationID, checkpointID)
		if err != nil {
			return nil, err
		}
		if !changed {
			return nil, nil
		}
		return func() {
			_, _ = s.LLMService.WithdrawProviderSecretTrust(context.WithoutCancel(ctx), trust.ProviderID, trust.DestinationID, checkpointID)
		}, nil
	case hitl.AuthorityAskQuiet:
		if delta.AskQuiet == nil {
			return nil, fmt.Errorf("ask-quiet record is missing")
		}
		installed, created := s.Gate.PutAskQuiet(hitl.AskQuiet{
			ID:               delta.AskQuiet.ID,
			ElevatedEffects:  delta.AskQuiet.ElevatedEffects,
			OwnerOperationID: checkpointID,
			ChatSessionID:    delta.ChatSessionID,
			Key:              delta.AskQuiet.Key,
			Label:            delta.AskQuiet.Label,
		}, delta.TTLSeconds)
		if !created {
			return nil, nil
		}
		id := installed.ID
		return func() { _ = s.Gate.RevokeAskQuietInstalledBy(id, checkpointID) }, nil
	default:
		return nil, fmt.Errorf("unsupported approval authority delta %q", delta.Kind)
	}
}

// RollbackApprovalOption revokes every authority delta in reverse order.
func (s *Handler) RollbackApprovalOption(ctx context.Context, checkpointID string, option hitl.ApprovalOption) error {
	for i := len(option.Authority) - 1; i >= 0; i-- {
		delta := option.Authority[i]
		switch delta.Kind {
		case hitl.AuthorityCurrentAction:
		case hitl.AuthorityGenericGrant:
			if delta.Grant != nil {
				_, _ = s.Gate.RevokeGrantInstalledBy(delta.Grant.ID, checkpointID)
			}
		case hitl.AuthoritySocketPermit:
			if s.Sockets != nil {
				for _, target := range delta.Sockets {
					s.Sockets.RevokePermit(delta.SessionID, delta.ToolCallID, delta.ActionDigest,
						confine.SocketGrant{ApprovedPath: target.ApprovedPath, ResolvedPath: target.ResolvedPath})
				}
			}
		case hitl.AuthoritySocketChat:
			if s.Sockets != nil && delta.Grant != nil {
				_, _ = s.Sockets.RevokeByIDInstalledBy(delta.Grant.ID, checkpointID)
			}
		case hitl.AuthorityDirectIPPermit:
			if s.DirectIP != nil && delta.DirectIPLease != nil {
				s.DirectIP.RevokePermit(delta.SessionID, delta.ToolCallID, delta.DirectIPLease.ActionDigest)
			}
		case hitl.AuthorityDirectIPChat:
			if s.DirectIP != nil && delta.Grant != nil {
				_, _ = s.DirectIP.RevokeByIDInstalledBy(delta.Grant.ID, checkpointID)
			}
		case hitl.AuthorityGrantedPath:
			if s.GrantedPaths != nil && delta.Grant != nil {
				s.GrantedPaths.RevokeInstalledBy(delta.ChatSession(), delta.Grant.ID, checkpointID)
			}
		case hitl.AuthorityWriteRootChat:
			if s.WriteRoots != nil && delta.Grant != nil {
				_, _ = s.WriteRoots.RevokeByIDInstalledBy(delta.Grant.ID, checkpointID)
			}
		case hitl.AuthorityReadPathChat:
			if s.ReadPaths != nil && delta.Grant != nil {
				_, _ = s.ReadPaths.RevokeByIDInstalledBy(delta.Grant.ID, checkpointID)
			}
		case hitl.AuthorityLocalListenChat:
			if s.Listen != nil && delta.Grant != nil {
				_, _ = s.Listen.RevokeByIDInstalledBy(delta.Grant.ID, checkpointID)
			}
		case hitl.AuthorityLoopbackConnectChat:
			if s.Loopback != nil && delta.Grant != nil {
				_, _ = s.Loopback.RevokeByIDInstalledBy(delta.Grant.ID, checkpointID)
			}
		case hitl.AuthorityAskQuiet:
			if delta.AskQuiet != nil {
				s.Gate.RevokeAskQuietInstalledBy(delta.AskQuiet.ID, checkpointID)
			}
		case hitl.AuthorityTrustDestination:
			if s.LLMService != nil && delta.TrustDestination != nil {
				if _, err := s.LLMService.WithdrawProviderSecretTrust(ctx, delta.TrustDestination.ProviderID, delta.TrustDestination.DestinationID, checkpointID); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unsupported approval authority delta %q", delta.Kind)
		}
	}
	return nil
}
