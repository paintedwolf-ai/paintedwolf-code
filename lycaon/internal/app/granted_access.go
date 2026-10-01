package app

import (
	"strings"

	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
)

func durableGrantedPaths(leases []hitl.ApprovalGrant, projectID string) []grantedpath.Grant {
	var out []grantedpath.Grant
	for _, lease := range leases {
		if lease.GrantedPath == nil || strings.TrimSpace(lease.GrantedPath.Path) == "" {
			continue
		}
		switch lease.Scope {
		case hitl.ApprovalGrantScopeProject:
			if strings.TrimSpace(lease.ProjectID) == "" || strings.TrimSpace(lease.ProjectID) != strings.TrimSpace(projectID) {
				continue
			}
		case hitl.ApprovalGrantScopeDevice:
		default:
			continue
		}
		mode := grantedpath.ModeRead
		if lease.GrantedPath.Write {
			mode = grantedpath.ModeWrite
		}
		out = append(out, grantedpath.Grant{
			ID: lease.ID, Path: lease.GrantedPath.Path, Mode: mode,
			Tree: lease.GrantedPath.Tree, ExpiresAt: lease.ExpiresAt,
		})
	}
	return out
}
