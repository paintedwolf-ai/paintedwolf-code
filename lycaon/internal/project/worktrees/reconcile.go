package worktrees

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
)

type observedCheckout struct {
	entry   git.WorktreeEntry
	inspect gitrepo.RegisteredCheckout
}

func (inv *Inventory) reconcileRepository(ctx context.Context, spec RepoSpec) ([]Checkout, error) {
	toplevel := canonicalPath(spec.Toplevel)
	commonDir, _, _, ok := gitrepo.CommonRepository(toplevel)
	if !ok || commonDir == "" {
		return nil, fmt.Errorf("resolve repository common directory for %s", toplevel)
	}
	entries, err := inv.git.ListWorktrees(ctx, toplevel)
	if err != nil {
		return nil, err
	}

	observed := make([]observedCheckout, 0, len(entries))
	seenPaths := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.Bare {
			// Bare common repositories expose only working directories as editable checkouts.
			continue
		}
		inspected := inv.inspector(commonDir, entry.Path)
		if inspected.Root == "" {
			continue
		}
		if _, dup := seenPaths[inspected.Root]; dup {
			continue
		}
		seenPaths[inspected.Root] = struct{}{}
		observed = append(observed, observedCheckout{
			entry:   entry,
			inspect: inspected,
		})
	}

	key := repoKey{projectID: spec.ProjectID, repoID: spec.RepoID}
	inv.mu.Lock()
	witnessMap := inv.witnesses[key]
	pendingMoves := append([]MoveReceipt(nil), inv.moves[key]...)
	inv.mu.Unlock()

	now := inv.now().UTC()
	nowText := formatTime(now)

	tx, err := inv.database.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := db.New(tx)

	existing, err := q.ListActiveSourceWorktreesByRepo(ctx, db.ListActiveSourceWorktreesByRepoParams{
		ProjectID: spec.ProjectID,
		RepoID:    spec.RepoID,
	})
	if err != nil {
		return nil, err
	}

	plan := matchCheckouts(spec, toplevel, existing, observed, witnessMap, pendingMoves)

	// 1. Retire rows that disappeared or lost continuity first so partial unique indexes stay clear.
	for _, ret := range plan.retire {
		if err := q.RetireSourceWorktree(ctx, db.RetireSourceWorktreeParams{
			RetiredAt:      nowText,
			UpdatedAt:      nowText,
			SupersededByID: ret.supersededByID,
			ProjectID:      spec.ProjectID,
			ID:             ret.id,
		}); err != nil {
			return nil, err
		}
	}

	// 2. Park any path-changing live updates at unique temporary paths before final assignment.
	for _, upd := range plan.updates {
		if upd.oldPath != upd.params.WorktreePath {
			park := upd.params
			park.WorktreePath = ".moving:" + upd.params.ID
			park.UpdatedAt = nowText
			if err := q.UpdateSourceWorktreeLive(ctx, park); err != nil {
				return nil, err
			}
		}
	}

	// 3. Apply live updates.
	for _, upd := range plan.updates {
		params := upd.params
		params.UpdatedAt = nowText
		if err := q.UpdateSourceWorktreeLive(ctx, params); err != nil {
			return nil, err
		}
	}

	// 4. Insert newly discovered or replacement incarnations.
	for _, ins := range plan.inserts {
		ins.CreatedAt = nowText
		ins.UpdatedAt = nowText
		if err := q.InsertSourceWorktree(ctx, ins); err != nil {
			return nil, err
		}
	}

	activeRows, err := q.ListActiveSourceWorktreesByRepo(ctx, db.ListActiveSourceWorktreesByRepoParams{
		ProjectID: spec.ProjectID,
		RepoID:    spec.RepoID,
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	out := make([]Checkout, 0, len(activeRows))
	nextWitnesses := make(map[string]liveWitness, len(activeRows))
	for _, row := range activeRows {
		c := checkoutFromRow(row)
		out = append(out, c)
		nextWitnesses[c.ID] = liveWitness{
			worktreePath:     c.WorktreePath,
			adminDir:         c.AdminDir,
			adminIdentity:    c.AdminIdentity,
			worktreeIdentity: c.WorktreeIdentity,
			available:        c.Available,
		}
	}

	inv.mu.Lock()
	inv.witnesses[key] = nextWitnesses
	if len(pendingMoves) > 0 && len(inv.moves[key]) >= len(pendingMoves) {
		inv.moves[key] = inv.moves[key][len(pendingMoves):]
	} else {
		delete(inv.moves, key)
	}
	inv.mu.Unlock()

	return out, nil
}

type retireAction struct {
	id             string
	supersededByID string
}

type updateAction struct {
	oldPath string
	params  db.UpdateSourceWorktreeLiveParams
}

type reconcilePlan struct {
	retire  []retireAction
	updates []updateAction
	inserts []db.InsertSourceWorktreeParams
}

func matchCheckouts(
	spec RepoSpec,
	toplevel string,
	existing []db.SourceWorktrees,
	observed []observedCheckout,
	witnesses map[string]liveWitness,
	moves []MoveReceipt,
) reconcilePlan {
	matchedExisting := make([]bool, len(existing))
	matchedObserved := make([]int, len(observed))
	for i := range matchedObserved {
		matchedObserved[i] = -1
	}

	// Pass 1: Verified move receipts.
	for obsIdx, obs := range observed {
		for exIdx, ex := range existing {
			if matchedExisting[exIdx] {
				continue
			}
			if matchesMoveReceipt(ex, obs, moves) {
				matchedExisting[exIdx] = true
				matchedObserved[obsIdx] = exIdx
				break
			}
		}
	}

	// Pass 2: Proven incarnation continuity at unchanged path.
	for obsIdx, obs := range observed {
		if matchedObserved[obsIdx] != -1 {
			continue
		}
		for exIdx, ex := range existing {
			if matchedExisting[exIdx] || canonicalPath(ex.WorktreePath) != obs.inspect.Root {
				continue
			}
			if provesSamePathContinuity(ex, obs, witnesses) {
				matchedExisting[exIdx] = true
				matchedObserved[obsIdx] = exIdx
				break
			}
		}
	}

	// Pass 3: Proven incarnation continuity across moved path.
	for obsIdx, obs := range observed {
		if matchedObserved[obsIdx] != -1 {
			continue
		}
		for exIdx, ex := range existing {
			if matchedExisting[exIdx] {
				continue
			}
			if provesMovedContinuity(ex, obs, witnesses) {
				matchedExisting[exIdx] = true
				matchedObserved[obsIdx] = exIdx
				break
			}
		}
	}

	// Pass 4: First-reconciliation legacy or pre-registered row adoption.
	for obsIdx, obs := range observed {
		if matchedObserved[obsIdx] != -1 {
			continue
		}
		for exIdx, ex := range existing {
			if matchedExisting[exIdx] {
				continue
			}
			if canAdoptLegacyRow(ex, obs) {
				matchedExisting[exIdx] = true
				matchedObserved[obsIdx] = exIdx
				break
			}
		}
	}

	var plan reconcilePlan
	replacementByPath := make(map[string]string)
	replacementByAdmin := make(map[string]string)

	for obsIdx, obs := range observed {
		exIdx := matchedObserved[obsIdx]
		if exIdx >= 0 {
			ex := existing[exIdx]
			plan.updates = append(plan.updates, updateAction{
				oldPath: ex.WorktreePath,
				params:  buildUpdateParams(spec.ProjectID, toplevel, ex, obs),
			})
			continue
		}
		freshID := uuid.NewString()
		replacementByPath[obs.inspect.Root] = freshID
		if obs.inspect.AdminDir != "" {
			replacementByAdmin[obs.inspect.AdminDir] = freshID
		}
		plan.inserts = append(plan.inserts, buildInsertParams(spec, toplevel, freshID, obs))
	}

	for exIdx, ex := range existing {
		if matchedExisting[exIdx] {
			continue
		}
		supersededBy := replacementByPath[canonicalPath(ex.WorktreePath)]
		if supersededBy == "" && ex.AdminDir != "" {
			supersededBy = replacementByAdmin[canonicalPath(ex.AdminDir)]
		}
		plan.retire = append(plan.retire, retireAction{
			id:             ex.ID,
			supersededByID: supersededBy,
		})
	}
	return plan
}

func matchesMoveReceipt(ex db.SourceWorktrees, obs observedCheckout, moves []MoveReceipt) bool {
	if len(moves) == 0 {
		return false
	}
	exPath := canonicalPath(ex.WorktreePath)
	for _, m := range moves {
		if m.ID != ex.ID || m.FromPath != exPath || m.ToPath != obs.inspect.Root {
			continue
		}
		if fspath.HasCreationDiscriminator(ex.AdminIdentity) && fspath.HasCreationDiscriminator(obs.inspect.AdminIdentity) {
			return fspath.SameIncarnation(ex.AdminIdentity, obs.inspect.AdminIdentity)
		}
		return true
	}
	return false
}

func provesSamePathContinuity(ex db.SourceWorktrees, obs observedCheckout, witnesses map[string]liveWitness) bool {
	if ex.AdminDir == "" || obs.inspect.AdminDir == "" {
		return false
	}
	if ex.Kind != string(obs.inspect.Kind) || canonicalPath(ex.AdminDir) != obs.inspect.AdminDir {
		return false
	}
	if fspath.SameIncarnation(ex.AdminIdentity, obs.inspect.AdminIdentity) {
		if !obs.inspect.Available {
			return true
		}
		if ex.WorktreeIdentity == "" {
			return true
		}
		return fspath.SameIncarnation(ex.WorktreeIdentity, obs.inspect.WorktreeIdentity)
	}
	// Without a filesystem creation discriminator, only continuous in-process observation proves continuity.
	if w, ok := witnesses[ex.ID]; ok && !fspath.HasCreationDiscriminator(ex.AdminIdentity) && !fspath.HasCreationDiscriminator(obs.inspect.AdminIdentity) {
		if w.adminIdentity != "" && w.adminIdentity == obs.inspect.AdminIdentity {
			if !obs.inspect.Available {
				return true
			}
			return w.available && w.worktreeIdentity != "" && w.worktreeIdentity == obs.inspect.WorktreeIdentity
		}
	}
	return false
}

func provesMovedContinuity(ex db.SourceWorktrees, obs observedCheckout, witnesses map[string]liveWitness) bool {
	if ex.AdminDir == "" || obs.inspect.AdminDir == "" || ex.Kind != string(obs.inspect.Kind) {
		return false
	}
	if canonicalPath(ex.AdminDir) != obs.inspect.AdminDir {
		return false
	}
	if fspath.SameIncarnation(ex.AdminIdentity, obs.inspect.AdminIdentity) {
		if !obs.inspect.Available || ex.WorktreeIdentity == "" {
			return true
		}
		// Same-filesystem rename preserves both admin and worktree directory incarnations.
		return fspath.SameIncarnation(ex.WorktreeIdentity, obs.inspect.WorktreeIdentity)
	}
	if w, ok := witnesses[ex.ID]; ok && !fspath.HasCreationDiscriminator(ex.AdminIdentity) && !fspath.HasCreationDiscriminator(obs.inspect.AdminIdentity) {
		return w.available && obs.inspect.Available &&
			w.adminIdentity != "" && w.adminIdentity == obs.inspect.AdminIdentity &&
			w.worktreeIdentity != "" && w.worktreeIdentity == obs.inspect.WorktreeIdentity
	}
	return false
}

func canAdoptLegacyRow(ex db.SourceWorktrees, obs observedCheckout) bool {
	if ex.AdminIdentity != "" || ex.CommonDir != "" {
		return false
	}
	if !obs.inspect.Available || canonicalPath(ex.WorktreePath) != obs.inspect.Root {
		return false
	}
	if ex.Branch != "" && (obs.entry.Detached || obs.entry.Branch != ex.Branch) {
		return false
	}
	return true
}

func buildUpdateParams(projectID, toplevel string, ex db.SourceWorktrees, obs observedCheckout) db.UpdateSourceWorktreeLiveParams {
	worktreeID := obs.inspect.WorktreeIdentity
	if !obs.inspect.Available && worktreeID == "" {
		worktreeID = ex.WorktreeIdentity
	}
	return db.UpdateSourceWorktreeLiveParams{
		Toplevel:         toplevel,
		WorktreePath:     obs.inspect.Root,
		Branch:           obs.entry.Branch,
		Kind:             string(obs.inspect.Kind),
		Available:        boolInt(obs.inspect.Available),
		Managed:          ex.Managed,
		CommonDir:        obs.inspect.CommonDir,
		AdminDir:         obs.inspect.AdminDir,
		CommonIdentity:   obs.inspect.CommonIdentity,
		AdminIdentity:    obs.inspect.AdminIdentity,
		WorktreeIdentity: worktreeID,
		HeadSha:          obs.entry.HeadSHA,
		Detached:         boolInt(obs.entry.Detached),
		Locked:           boolInt(obs.entry.Locked),
		LockReason:       obs.entry.LockReason,
		Prunable:         boolInt(obs.entry.Prunable),
		PruneReason:      obs.entry.PruneReason,
		UpdatedAt:        ex.UpdatedAt,
		ProjectID:        projectID,
		ID:               ex.ID,
	}
}

func buildInsertParams(spec RepoSpec, toplevel, id string, obs observedCheckout) db.InsertSourceWorktreeParams {
	return db.InsertSourceWorktreeParams{
		ID:               id,
		ProjectID:        spec.ProjectID,
		RepoID:           spec.RepoID,
		Toplevel:         toplevel,
		WorktreePath:     obs.inspect.Root,
		Branch:           obs.entry.Branch,
		BaseBranch:       "",
		State:            "active",
		Kind:             string(obs.inspect.Kind),
		Available:        boolInt(obs.inspect.Available),
		Managed:          0,
		CommonDir:        obs.inspect.CommonDir,
		AdminDir:         obs.inspect.AdminDir,
		CommonIdentity:   obs.inspect.CommonIdentity,
		AdminIdentity:    obs.inspect.AdminIdentity,
		WorktreeIdentity: obs.inspect.WorktreeIdentity,
		HeadSha:          obs.entry.HeadSHA,
		Detached:         boolInt(obs.entry.Detached),
		Locked:           boolInt(obs.entry.Locked),
		LockReason:       obs.entry.LockReason,
		Prunable:         boolInt(obs.entry.Prunable),
		PruneReason:      obs.entry.PruneReason,
	}
}
