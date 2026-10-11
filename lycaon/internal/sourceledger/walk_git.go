package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
)

// Git movements and file effects consume the same bounded history page.
func (s *Walk) walkPage(ctx context.Context, projectID string, baseline Baseline, limit int, before int64) ([]Effect, []GitTransition, int64, error) {
	// Both streams share a ceiling: commits arriving between reads belong to
	// the next refresh, never a page whose file read has already completed.
	latest, err := s.queries.LatestSourceOrdinal(ctx, projectID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, 0, err
	}
	if before == 0 || before > latest+1 {
		before = latest + 1
	}
	effects, err := s.queryEffects(ctx, projectID, baseline, limit+1, before)
	if err != nil {
		return nil, nil, 0, err
	}
	git, err := s.queryWalkGit(ctx, projectID, baseline, limit+1, before)
	if err != nil {
		return nil, nil, 0, err
	}
	ordinals := make([]int64, 0, len(effects)+len(git))
	for _, effect := range effects {
		ordinals = append(ordinals, effect.Ordinal)
	}
	for _, change := range git {
		ordinals = append(ordinals, change.Ordinal)
	}
	slices.SortFunc(ordinals, func(a, b int64) int {
		if a > b {
			return -1
		}
		if a < b {
			return 1
		}
		return 0
	})
	var next int64
	if len(ordinals) > limit {
		next = ordinals[limit-1]
		effects = slices.DeleteFunc(effects, func(e Effect) bool { return e.Ordinal < next })
		git = slices.DeleteFunc(git, func(g GitTransition) bool { return g.Ordinal < next })
	}
	// References may precede the page. They enrich their effects without moving its cursor.
	known := make(map[string]bool, len(git))
	for _, change := range git {
		known[change.ID] = true
	}
	ids := make([]string, 0)
	for _, effect := range effects {
		if effect.GitTransitionID != "" && !known[effect.GitTransitionID] {
			ids = append(ids, effect.GitTransitionID)
			known[effect.GitTransitionID] = true
		}
	}
	referenced, err := s.git.GitTransitionsByIDs(ctx, ids)
	if err != nil {
		return nil, nil, 0, err
	}
	for _, change := range referenced {
		if change.ProjectID != projectID {
			return nil, nil, 0, fmt.Errorf("a Git reference crossed project history")
		}
		git = append(git, change)
	}
	sort.Slice(git, func(i, j int) bool { return git[i].Ordinal > git[j].Ordinal })
	return effects, git, next, nil
}

func (s *Walk) queryWalkGit(ctx context.Context, projectID string, baseline Baseline, limit int, before int64) ([]GitTransition, error) {
	roots, err := encodeRootBranches(baseline.RootBranches)
	if err != nil {
		return nil, err
	}
	query := `SELECT g.id FROM source_git_transitions g WHERE g.project_id = ?
 AND (? = '' OR EXISTS (SELECT 1 FROM json_each(?) r WHERE r.key=g.root_id AND r.value=g.branch_id))
 AND (? = 0 OR g.ordinal < ?)`
	args := []any{projectID, roots, rootsJSON(roots), before, before}
	switch baseline.Kind {
	case BaselineSession, BaselineTurn:
		query += ` AND ((g.session_id<>'' AND g.session_id=?`
		args = append(args, baseline.SessionID)
		if baseline.Kind == BaselineTurn {
			query += ` AND g.turn=?`
			args = append(args, baseline.Turn)
		}
		query += `)`
		if baseline.Kind == BaselineSession && baseline.WithOutsideChanges {
			query += ` OR (g.session_id='' AND NOT EXISTS (
 SELECT 1 FROM source_operations o WHERE o.git_transition_id=g.id AND o.session_id<>'')
 AND EXISTS (SELECT 1 FROM sessions s WHERE s.id=? AND s.project_id=g.project_id
 AND julianday(g.observed_ts)>=julianday(s.created_at)
 AND (s.archived_at IS NULL OR julianday(g.observed_ts)<=julianday(s.archived_at))))`
			args = append(args, baseline.SessionID)
		}
		query += `)`
	case BaselinePin:
		ordinal, err := s.resolvePinOrdinal(ctx, projectID, baseline)
		if err != nil {
			return nil, err
		}
		query += ` AND g.ordinal > ?`
		args = append(args, ordinal)
	case BaselinePresentation:
		query += ` AND (g.branch_id='' OR substr(g.branch_id,1,9)='worktree:')`
	case BaselineCommit:
	}
	query += ` ORDER BY g.ordinal DESC LIMIT ?`
	args = append(args, limit)
	ids, err := s.queryTransitionIDs(ctx, query, args, limit)
	if err != nil {
		return nil, err
	}
	byID, err := s.git.GitTransitionsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]GitTransition, 0, len(ids))
	for _, id := range ids {
		result = append(result, byID[id])
	}
	return result, nil
}

func rootsJSON(roots string) string {
	if roots == "" {
		return "{}"
	}
	return roots
}

// queryTransitionIDs reads the ids and closes its rows before the caller's next query.
func (s *Walk) queryTransitionIDs(ctx context.Context, query string, args []any, limit int) ([]string, error) {
	rows, err := s.sqlDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
