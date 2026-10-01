package app

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
)

func TestDurableGrantedPathsRespectScope(t *testing.T) {
	for _, scope := range []hitl.ApprovalGrantScope{
		hitl.ApprovalGrantScopeChat, hitl.ApprovalGrantScopeProject,
		hitl.ApprovalGrantScopeDevice, "unknown", "",
	} {
		for _, projectID := range []string{"project-a", "project-b", ""} {
			t.Run(string(scope)+"/"+projectID, func(t *testing.T) {
				expires := time.Now().Add(time.Hour)
				lease := hitl.ApprovalGrant{
					ID: "grant_path", Scope: scope, ProjectID: "project-a", ChatSessionID: "chat-a",
					GrantedPath: &hitl.GrantedPathDelta{Path: "/outside/file", Write: true, Tree: true},
					ExpiresAt:   &expires,
				}
				got := durableGrantedPaths([]hitl.ApprovalGrant{lease}, projectID)
				allowed := scope == hitl.ApprovalGrantScopeDevice ||
					(scope == hitl.ApprovalGrantScopeProject && projectID == "project-a")
				if !allowed {
					if len(got) != 0 {
						t.Fatalf("scope %q escaped into project %q: %+v", scope, projectID, got)
					}
					return
				}
				if len(got) != 1 || got[0].ID != lease.ID || got[0].Mode != grantedpath.ModeWrite ||
					!got[0].Tree || got[0].Path != lease.GrantedPath.Path || got[0].ExpiresAt != lease.ExpiresAt {
					t.Fatalf("durable authority changed: %+v", got)
				}
			})
		}
	}
}
