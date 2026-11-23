package worktrees

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/tools"
)

// ToolUsageRecorder adapts Inventory to tools.CheckoutUsageRecorder without failing tool calls on background discovery errors.
type ToolUsageRecorder struct {
	Inventory *Inventory
}

// NewToolUsageRecorder wraps inv as a tools.CheckoutUsageRecorder.
func NewToolUsageRecorder(inv *Inventory) *ToolUsageRecorder {
	if inv == nil {
		return nil
	}
	return &ToolUsageRecorder{Inventory: inv}
}

func (r *ToolUsageRecorder) RecordCheckoutActivity(ctx context.Context, tc tools.ToolContext, targetPath string, activityID string, relation tools.CheckoutUsageRelation) string {
	if r == nil || r.Inventory == nil {
		return ""
	}
	rel := UsageRelationUsed
	if relation == tools.CheckoutUsageCreated {
		rel = UsageRelationCreated
	}
	if activityID == "" {
		activityID = strings.TrimSpace(tc.Identity.ToolCallID)
	}
	workerID := strings.TrimSpace(tc.Identity.WorkerJobID)
	rootSessionID := tc.ChatSessionID()
	actorSessionID := strings.TrimSpace(tc.Identity.SessionID)
	if workerID == "" && rootSessionID != "" && actorSessionID != "" && actorSessionID != rootSessionID {
		workerID = strings.TrimSpace(tc.Identity.HandoffAgentID)
		if workerID == "" {
			workerID = actorSessionID
		}
	}
	out, err := r.Inventory.RecordUsage(ctx, UsageInput{
		ProjectID:        tc.Identity.ProjectID,
		WorkerBranchRoot: tc.Source.WorkerBranchRoot,
		TargetPath:       targetPath,
		RootSessionID:    rootSessionID,
		ActorSessionID:   actorSessionID,
		WorkerID:         workerID,
		ActivityID:       activityID,
		Relation:         rel,
	})
	if err != nil {
		slog.DebugContext(ctx, "record checkout activity", "project_id", tc.Identity.ProjectID, "err", err)
		return ""
	}
	if !out.Recorded {
		return ""
	}
	return out.Checkout.ID
}

func (r *ToolUsageRecorder) NotifyCheckoutChange(ctx context.Context, tc tools.ToolContext, repoPath string) {
	if r == nil || r.Inventory == nil {
		return
	}
	r.Inventory.ScheduleRepoRefreshForPath(ctx, tc.Identity.ProjectID, repoPath)
}

// ScheduleRepoRefreshForProject schedules asynchronous checkout reconciliation for every attached Git repository of projectID.
func (inv *Inventory) ScheduleRepoRefreshForProject(ctx context.Context, projectID string) {
	if inv == nil {
		return
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return
	}
	roots, err := db.New(inv.database).ListProjectRoots(ctx, projectID)
	if err != nil {
		return
	}
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		repo, ok := gitrepo.Discover(root.Path)
		if !ok || repo.Root == "" {
			continue
		}
		top := canonicalPath(repo.Root)
		if top == "" {
			continue
		}
		if _, dup := seen[top]; dup {
			continue
		}
		seen[top] = struct{}{}
		if repoID := git.DeriveRepoID(top); repoID != "" {
			inv.ScheduleRefresh(ctx, RepoSpec{
				ProjectID: projectID,
				RepoID:    repoID,
				Toplevel:  top,
			})
		}
	}
}
