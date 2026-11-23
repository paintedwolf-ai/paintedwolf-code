package worktrees

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
)

// UsageRelation distinguishes checkout creation from later activity inside an existing checkout.
type UsageRelation string

const (
	UsageRelationCreated UsageRelation = "created"
	UsageRelationUsed    UsageRelation = "used"
)

// UsageInput describes one verified actor activity inside or creating a checkout.
type UsageInput struct {
	ProjectID        string
	WorkerBranchRoot string
	TargetPath       string
	RootSessionID    string
	ActorSessionID   string
	WorkerID         string
	ActivityID       string
	Relation         UsageRelation
}

// UsageOutcome reports the checkout association recorded for an activity.
type UsageOutcome struct {
	Recorded  bool
	Duplicate bool
	Checkout  Checkout
	RepoSpec  RepoSpec
}

// ChatCheckoutUsage summarizes a chat's recorded creation and use of one checkout.
type ChatCheckoutUsage struct {
	ProjectID       string
	RepoID          string
	WorktreeID      string
	CreatedByChat   bool
	UsedByChat      bool
	ActorSessionIDs []string
	WorkerIDs       []string
	FirstActivityID string
	LastActivityID  string
	FirstTS         time.Time
	LastTS          time.Time
}

// EnsureRegisteredCheckout resolves or inserts a verified live registration without invoking Git subprocesses.
func (inv *Inventory) EnsureRegisteredCheckout(ctx context.Context, spec RepoSpec, reg gitrepo.RegisteredCheckout) (Checkout, bool, error) {
	toplevel := canonicalPath(spec.Toplevel)
	if toplevel == "" || !reg.Available || reg.Root == "" || reg.CommonDir == "" {
		return Checkout{}, false, ErrCheckoutUnavailable
	}
	branch, headSHA, detached := gitrepo.ReadCheckoutHead(reg.CommonDir, reg.AdminDir)
	nowText := formatTime(inv.now().UTC())

	tx, err := inv.database.BeginTx(ctx, nil)
	if err != nil {
		return Checkout{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	existing, err := q.ListActiveSourceWorktreesByRepo(ctx, db.ListActiveSourceWorktreesByRepoParams{
		ProjectID: spec.ProjectID,
		RepoID:    spec.RepoID,
	})
	if err != nil {
		return Checkout{}, false, err
	}

	key := repoKey{projectID: spec.ProjectID, repoID: spec.RepoID}
	inv.mu.Lock()
	witnessMap := inv.witnesses[key]
	inv.mu.Unlock()

	for _, row := range existing {
		if rowMatchesRegisteredIncarnation(row, reg, branch, detached, witnessMap) {
			if canonicalPath(row.WorktreePath) != reg.Root || row.AdminDir == "" || row.AdminIdentity == "" {
				upd := db.UpdateSourceWorktreeLiveParams{
					Toplevel:         toplevel,
					WorktreePath:     reg.Root,
					Branch:           chooseNonEmpty(branch, row.Branch),
					Kind:             string(reg.Kind),
					Available:        1,
					CommonDir:        reg.CommonDir,
					AdminDir:         reg.AdminDir,
					CommonIdentity:   reg.CommonIdentity,
					AdminIdentity:    reg.AdminIdentity,
					WorktreeIdentity: reg.WorktreeIdentity,
					HeadSha:          chooseNonEmpty(headSHA, row.HeadSha),
					Detached:         boolToInt(detached),
					Locked:           row.Locked,
					LockReason:       row.LockReason,
					Prunable:         0,
					PruneReason:      "",
					UpdatedAt:        nowText,
					ProjectID:        spec.ProjectID,
					ID:               row.ID,
				}
				if err := q.UpdateSourceWorktreeLive(ctx, upd); err != nil {
					return Checkout{}, false, err
				}
				reloaded, err := q.GetSourceWorktreeByID(ctx, db.GetSourceWorktreeByIDParams{ProjectID: spec.ProjectID, ID: row.ID})
				if err != nil {
					return Checkout{}, false, err
				}
				row = reloaded
			}
			if err := tx.Commit(); err != nil {
				return Checkout{}, false, err
			}
			c := checkoutFromRow(row)
			inv.recordWitness(key, c)
			return c, false, nil
		}
	}

	newID := uuid.NewString()
	for _, row := range existing {
		samePath := canonicalPath(row.WorktreePath) == reg.Root
		sameAdmin := reg.AdminDir != "" && row.AdminDir != "" && canonicalPath(row.AdminDir) == reg.AdminDir
		if samePath || sameAdmin {
			if err := q.RetireSourceWorktree(ctx, db.RetireSourceWorktreeParams{
				RetiredAt:      nowText,
				UpdatedAt:      nowText,
				SupersededByID: newID,
				ProjectID:      spec.ProjectID,
				ID:             row.ID,
			}); err != nil {
				return Checkout{}, false, err
			}
		}
	}

	ins := db.InsertSourceWorktreeParams{
		ID:               newID,
		ProjectID:        spec.ProjectID,
		RepoID:           spec.RepoID,
		Toplevel:         toplevel,
		WorktreePath:     reg.Root,
		Branch:           branch,
		BaseBranch:       "",
		CreatedAt:        nowText,
		State:            "active",
		Kind:             string(reg.Kind),
		Available:        1,
		Managed:          0,
		CommonDir:        reg.CommonDir,
		AdminDir:         reg.AdminDir,
		CommonIdentity:   reg.CommonIdentity,
		AdminIdentity:    reg.AdminIdentity,
		WorktreeIdentity: reg.WorktreeIdentity,
		HeadSha:          headSHA,
		Detached:         boolToInt(detached),
		Locked:           0,
		LockReason:       "",
		Prunable:         0,
		PruneReason:      "",
		UpdatedAt:        nowText,
	}
	if err := q.InsertSourceWorktree(ctx, ins); err != nil {
		return Checkout{}, false, err
	}
	inserted, err := q.GetSourceWorktreeByID(ctx, db.GetSourceWorktreeByIDParams{ProjectID: spec.ProjectID, ID: newID})
	if err != nil {
		return Checkout{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Checkout{}, false, err
	}
	c := checkoutFromRow(inserted)
	inv.recordWitness(key, c)
	return c, true, nil
}

// RecordUsage associates an authorized command or file effect with its verified registered checkout.
func (inv *Inventory) RecordUsage(ctx context.Context, in UsageInput) (UsageOutcome, error) {
	projectID := strings.TrimSpace(in.ProjectID)
	rootSessionID := strings.TrimSpace(in.RootSessionID)
	actorSessionID := strings.TrimSpace(in.ActorSessionID)
	workerID := strings.TrimSpace(in.WorkerID)
	activityID := strings.TrimSpace(in.ActivityID)
	targetPath := strings.TrimSpace(in.TargetPath)
	if actorSessionID == "" {
		actorSessionID = rootSessionID
	}
	if rootSessionID == "" {
		rootSessionID = actorSessionID
	}
	if projectID == "" || rootSessionID == "" || actorSessionID == "" || targetPath == "" {
		return UsageOutcome{}, nil
	}
	if branchRoot := canonicalPath(in.WorkerBranchRoot); branchRoot != "" && isPathInside(targetPath, branchRoot) {
		return UsageOutcome{}, nil
	}

	reg, ok := gitrepo.ContainingRegisteredCheckout(targetPath)
	if !ok {
		return UsageOutcome{}, nil
	}

	spec, matched, err := inv.matchAttachedRepository(ctx, projectID, reg.CommonDir)
	if err != nil || !matched {
		return UsageOutcome{}, err
	}

	checkout, inserted, err := inv.EnsureRegisteredCheckout(ctx, spec, reg)
	if err != nil {
		return UsageOutcome{}, err
	}

	relation := in.Relation
	if relation != UsageRelationCreated {
		relation = UsageRelationUsed
	}

	nowText := formatTime(inv.now().UTC())
	tx, err := inv.database.BeginTx(ctx, nil)
	if err != nil {
		return UsageOutcome{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	if activityID != "" {
		rows, err := q.InsertSourceWorktreeUsageReceipt(ctx, db.InsertSourceWorktreeUsageReceiptParams{
			RootSessionID:  rootSessionID,
			ActivityID:     activityID,
			WorktreeID:     checkout.ID,
			Relation:       string(relation),
			ActorSessionID: actorSessionID,
			WorkerID:       workerID,
			ProjectID:      projectID,
			RepoID:         spec.RepoID,
			RecordedTs:     nowText,
		})
		if err != nil {
			return UsageOutcome{}, err
		}
		if rows == 0 {
			return UsageOutcome{Recorded: true, Duplicate: true, Checkout: checkout, RepoSpec: spec}, nil
		}
		if relation == UsageRelationCreated {
			_, _ = q.InsertSourceWorktreeUsageReceipt(ctx, db.InsertSourceWorktreeUsageReceiptParams{
				RootSessionID:  rootSessionID,
				ActivityID:     activityID,
				WorktreeID:     checkout.ID,
				Relation:       string(UsageRelationUsed),
				ActorSessionID: actorSessionID,
				WorkerID:       workerID,
				ProjectID:      projectID,
				RepoID:         spec.RepoID,
				RecordedTs:     nowText,
			})
		}
	}

	createdFlag := int64(0)
	if relation == UsageRelationCreated {
		createdFlag = 1
	}
	if err := q.UpsertSourceWorktreeUsage(ctx, db.UpsertSourceWorktreeUsageParams{
		ProjectID:       projectID,
		RepoID:          spec.RepoID,
		WorktreeID:      checkout.ID,
		RootSessionID:   rootSessionID,
		ActorSessionID:  actorSessionID,
		WorkerID:        workerID,
		CreatedByActor:  createdFlag,
		UsedByActor:     1,
		FirstActivityID: activityID,
		LastActivityID:  activityID,
		FirstTs:         nowText,
		LastTs:          nowText,
	}); err != nil {
		return UsageOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return UsageOutcome{}, err
	}

	if inserted || relation == UsageRelationCreated {
		inv.ScheduleRefresh(ctx, spec)
	}
	return UsageOutcome{Recorded: true, Checkout: checkout, RepoSpec: spec}, nil
}

// ScheduleRepoRefreshForPath schedules background checkout reconciliation if path belongs to an attached repository.
func (inv *Inventory) ScheduleRepoRefreshForPath(ctx context.Context, projectID, path string) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" || strings.TrimSpace(path) == "" {
		return
	}
	commonDir, _, bare, ok := gitrepo.CommonRepository(path)
	if !ok || bare || commonDir == "" {
		return
	}
	spec, matched, err := inv.matchAttachedRepository(ctx, projectID, commonDir)
	if err != nil || !matched {
		return
	}
	inv.ScheduleRefresh(ctx, spec)
}

// SummarizeChatRepoUsages returns per-checkout chat and worker usage summaries for a repository and root session.
func (inv *Inventory) SummarizeChatRepoUsages(ctx context.Context, projectID, repoID, rootSessionID string) (map[string]ChatCheckoutUsage, error) {
	projectID = strings.TrimSpace(projectID)
	repoID = strings.TrimSpace(repoID)
	rootSessionID = strings.TrimSpace(rootSessionID)
	if projectID == "" || repoID == "" || rootSessionID == "" {
		return map[string]ChatCheckoutUsage{}, nil
	}
	rows, err := db.New(inv.database).ListSourceWorktreeUsagesByRepoAndRootSession(ctx, db.ListSourceWorktreeUsagesByRepoAndRootSessionParams{
		ProjectID:     projectID,
		RepoID:        repoID,
		RootSessionID: rootSessionID,
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]ChatCheckoutUsage, len(rows))
	for _, row := range rows {
		firstTS, _ := db.ParseTime(row.FirstTs)
		lastTS, _ := db.ParseTime(row.LastTs)
		entry, exists := out[row.WorktreeID]
		if !exists {
			entry = ChatCheckoutUsage{
				ProjectID:       row.ProjectID,
				RepoID:          row.RepoID,
				WorktreeID:      row.WorktreeID,
				FirstActivityID: row.FirstActivityID,
				LastActivityID:  row.LastActivityID,
				FirstTS:         firstTS,
				LastTS:          lastTS,
			}
		}
		if row.CreatedByActor != 0 {
			entry.CreatedByChat = true
		}
		if row.UsedByActor != 0 {
			entry.UsedByChat = true
		}
		if row.ActorSessionID != "" && !slices.Contains(entry.ActorSessionIDs, row.ActorSessionID) {
			entry.ActorSessionIDs = append(entry.ActorSessionIDs, row.ActorSessionID)
		}
		if row.WorkerID != "" && !slices.Contains(entry.WorkerIDs, row.WorkerID) {
			entry.WorkerIDs = append(entry.WorkerIDs, row.WorkerID)
		}
		if !firstTS.IsZero() && (entry.FirstTS.IsZero() || firstTS.Before(entry.FirstTS)) {
			entry.FirstTS = firstTS
			entry.FirstActivityID = row.FirstActivityID
		}
		if lastTS.After(entry.LastTS) {
			entry.LastTS = lastTS
			entry.LastActivityID = row.LastActivityID
		}
		out[row.WorktreeID] = entry
	}
	return out, nil
}

func (inv *Inventory) matchAttachedRepository(ctx context.Context, projectID, commonDir string) (RepoSpec, bool, error) {
	roots, err := db.New(inv.database).ListProjectRoots(ctx, projectID)
	if err != nil {
		return RepoSpec{}, false, err
	}
	commonCanon := canonicalPath(commonDir)
	for _, root := range roots {
		repo, ok := gitrepo.Discover(root.Path)
		if !ok || repo.Root == "" || repo.CommonDir == "" {
			continue
		}
		if canonicalPath(repo.CommonDir) != commonCanon {
			continue
		}
		toplevel := canonicalPath(repo.Root)
		repoID := git.DeriveRepoID(toplevel)
		if toplevel != "" && repoID != "" {
			return RepoSpec{ProjectID: projectID, RepoID: repoID, Toplevel: toplevel}, true, nil
		}
	}
	return RepoSpec{}, false, nil
}

func (inv *Inventory) recordWitness(key repoKey, c Checkout) {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	if inv.witnesses[key] == nil {
		inv.witnesses[key] = make(map[string]liveWitness)
	}
	inv.witnesses[key][c.ID] = liveWitness{
		worktreePath:     c.WorktreePath,
		adminDir:         c.AdminDir,
		adminIdentity:    c.AdminIdentity,
		worktreeIdentity: c.WorktreeIdentity,
		available:        c.Available,
	}
}

func rowMatchesRegisteredIncarnation(row db.SourceWorktrees, reg gitrepo.RegisteredCheckout, branch string, detached bool, witnesses map[string]liveWitness) bool {
	if row.AdminDir != "" && reg.AdminDir != "" {
		if canonicalPath(row.AdminDir) != reg.AdminDir {
			return false
		}
		if matchesLiveIdentity(row.AdminIdentity, reg.AdminIdentity) {
			return true
		}
		if w, ok := witnesses[row.ID]; ok && w.adminDir == reg.AdminDir && w.adminIdentity != "" && w.adminIdentity == reg.AdminIdentity {
			return true
		}
		return false
	}
	// Legacy pre-evidence row at the exact path adopts only when branch state agrees.
	if row.AdminDir == "" && row.AdminIdentity == "" && canonicalPath(row.WorktreePath) == reg.Root {
		if !detached && row.Branch != "" && branch != "" && row.Branch == branch {
			return true
		}
	}
	return false
}

func chooseNonEmpty(preferred, fallback string) string {
	if preferred != "" {
		return preferred
	}
	return fallback
}

func boolToInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func isPathInside(candidate, root string) bool {
	candCanon := canonicalPath(candidate)
	if candCanon == "" {
		abs, err := filepath.Abs(candidate)
		if err != nil {
			return false
		}
		candCanon = filepath.Clean(abs)
	}
	if candCanon == root {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(candCanon, prefix)
}
