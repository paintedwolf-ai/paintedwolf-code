package worktrees

import (
	"context"
	"log/slog"
	"path/filepath"
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

func (r *ToolUsageRecorder) ResolvePathCheckout(ctx context.Context, projectID, targetPath string) (tools.ResolvedCheckoutPath, bool) {
	if r == nil || r.Inventory == nil {
		return tools.ResolvedCheckoutPath{}, false
	}
	projectID = strings.TrimSpace(projectID)
	targetPath = strings.TrimSpace(targetPath)
	if projectID == "" || targetPath == "" {
		return tools.ResolvedCheckoutPath{}, false
	}
	reg, ok := gitrepo.ContainingRegisteredCheckout(targetPath)
	if !ok {
		return tools.ResolvedCheckoutPath{}, false
	}
	spec, matched, err := r.Inventory.matchAttachedRepository(ctx, projectID, reg.CommonDir)
	if err != nil || !matched {
		return tools.ResolvedCheckoutPath{}, false
	}
	checkout, _, err := r.Inventory.EnsureRegisteredCheckout(ctx, spec, reg)
	if err != nil {
		return tools.ResolvedCheckoutPath{}, false
	}
	out := tools.ResolvedCheckoutPath{
		RepoID:     spec.RepoID,
		WorktreeID: checkout.ID,
	}
	roots, err := db.New(r.Inventory.database).ListProjectRoots(ctx, projectID)
	if err != nil {
		return out, true
	}
	absTarget := canonicalPath(targetPath)
	if absTarget == "" {
		absTarget = filepath.Clean(targetPath)
	}
	topCanon := canonicalPath(spec.Toplevel)
	wtCanon := canonicalPath(checkout.WorktreePath)
	var bestID, bestRel string
	bestLen := -1
	for _, root := range roots {
		rootCanon := canonicalPath(root.Path)
		if rootCanon == "" {
			continue
		}
		mappedPath := rootCanon
		if topCanon != "" && wtCanon != "" && isPathInside(rootCanon, topCanon) {
			if subRel, relErr := filepath.Rel(topCanon, rootCanon); relErr == nil {
				mappedPath = filepath.Join(wtCanon, subRel)
			}
		}
		if !isPathInside(absTarget, mappedPath) {
			continue
		}
		if len(mappedPath) > bestLen {
			rel, relErr := filepath.Rel(mappedPath, absTarget)
			if relErr == nil {
				bestLen = len(mappedPath)
				bestID = root.ID
				bestRel = filepath.ToSlash(rel)
			}
		}
	}
	out.RootID = bestID
	out.RootRel = bestRel
	return out, true
}

// ScheduleRepoRefreshForProject schedules background checkout reconciliation for projectID's repositories.
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
