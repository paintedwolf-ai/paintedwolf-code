package capabilityadmin

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
)

// RestoreUnavailableSocketIntent restores reviewable history without live grants.
func (s *Installation) RestoreUnavailableSocketIntent(ctx context.Context, saved hitl.ChatGrant) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	delta := saved.Delta
	if s.Sockets == nil || delta.Kind != hitl.AuthoritySocketChat || delta.Grant == nil || delta.ChatSession() == "" || saved.ID == "" {
		return fmt.Errorf("saved socket intent is invalid")
	}
	s.authorityMu.Lock()
	defer s.authorityMu.Unlock()
	if _, root, exists := s.Sockets.FindByID(saved.ID); exists && root != delta.ChatSession() {
		return fmt.Errorf("saved socket intent belongs to another chat")
	}
	for _, target := range delta.Sockets {
		s.Sockets.RestoreChatIntent(delta.ChatSession(), approvalstate.SocketChatGrant{
			ID: saved.ID, ApprovedPath: target.ApprovedPath, ResolvedPath: target.ResolvedPath, Binding: target.Binding,
			CreatedAt: delta.Grant.GrantedAt, ExpiresAt: saved.ExpiresAt,
			SourceCheckpointID: saved.CheckpointID, SourceActionDigest: delta.ActionDigest,
		})
	}
	return nil
}
