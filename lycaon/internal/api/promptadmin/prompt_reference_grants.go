package promptadmin

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/grantedpath"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Each submission owns its grants so a refused admission can revoke only its own access.
func (s *Submission) grantPromptReferences(ctx context.Context, sessionID, operationID string, parts []wire.MessageContentPart) ([]string, error) {
	root := sessiontree.RootID(ctx, s.Store, sessionID)
	if root == "" {
		root = sessionID
	}
	var installed []string
	for _, part := range parts {
		if part.ReferenceKind != wire.MessageReferenceKindExternalFile && part.ReferenceKind != wire.MessageReferenceKindExternalFolder {
			continue
		}
		if s.GrantedPaths == nil {
			s.revokePromptGrants(installed)
			return nil, fmt.Errorf("external reference authority is unavailable")
		}
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte("prompt-drop:"+root+":"+operationID+":"+string(part.ReferenceKind)+":"+part.Path)).String()
		added, err := s.GrantedPaths.Grant(root, grantedpath.Grant{
			ID: id, Path: part.Path, Mode: grantedpath.ModeWrite,
			Tree: part.ReferenceKind == wire.MessageReferenceKindExternalFolder,
		})
		if err != nil {
			s.revokePromptGrants(installed)
			return nil, err
		}
		if added {
			installed = append(installed, id)
		}
	}
	return installed, nil
}

func (s *Submission) revokePromptGrants(ids []string) {
	for _, id := range ids {
		s.GrantedPaths.RevokeByID(id)
	}
}
