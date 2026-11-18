package capabilityadmin

import (
	"cmp"
	"slices"
	"time"

	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// SourceLocationAccess projects live saved approvals for Files.
func (s *Inventory) SourceLocationAccess(chat, project, anchor string, exact bool) *wire.SourceLocationAccess {
	now := time.Now()
	grants := append(s.Gate.ListGrants(chat), s.writeRootGrantsForList(chat)...)
	var access []wire.SourceLocationAccess
	for _, grant := range grants {
		if grant.Unavailable || grant.ExpiresAt != nil && !grant.ExpiresAt.After(now) {
			continue
		}
		if grant.Scope == hitl.ApprovalGrantScopeProject && grant.ProjectID != project ||
			grant.Scope == hitl.ApprovalGrantScopeChat && grant.ChatSessionID != chat {
			continue
		}
		path := grant.GrantedPath
		if path == nil && grant.Predicate.Category == hitl.ApprovalGrantCategoryWriteRoot {
			path = &hitl.GrantedPathDelta{Path: grant.Predicate.Pattern, Write: true, Tree: true}
		}
		if path == nil || !grantedpath.CoversPath(path.Path, path.Tree, anchor) &&
			(exact || !grantedpath.CoversPath(anchor, true, path.Path)) {
			continue
		}
		mode := "read"
		if path.Write {
			mode = "write"
		}
		access = append(access, wire.SourceLocationAccess{Path: path.Path, Mode: mode, Tree: path.Tree, Scope: string(grant.Scope), ExpiresAt: grant.ExpiresAt})
	}
	if len(access) == 0 {
		return nil
	}
	slices.SortFunc(access, func(a, b wire.SourceLocationAccess) int {
		if mode := cmp.Compare(b.Mode, a.Mode); mode != 0 {
			return mode
		}
		if scope := cmp.Compare(a.Scope, b.Scope); scope != 0 {
			return scope
		}
		return cmp.Compare(a.Path, b.Path)
	})
	return &access[0]
}
