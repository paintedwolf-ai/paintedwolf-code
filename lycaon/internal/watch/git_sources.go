package watch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/git"
)

func gitRefCursorKey(altIdx int) string {
	return fmt.Sprintf("git_ref:state:%d", altIdx)
}

func resolveWatchRootGrant(auth Authority, rootLabel string) (WatchRootGrant, bool) {
	want := strings.TrimPrefix(strings.TrimSpace(rootLabel), "@")
	for _, r := range auth.Roots {
		if r.Label == want || r.RootID == want {
			return r, true
		}
	}
	if len(auth.RootPaths) == 1 && auth.RootPaths[0] != "" {
		return WatchRootGrant{Label: want, Path: auth.RootPaths[0]}, true
	}
	if auth.ProjectDir != "" && len(auth.Roots) == 0 {
		return WatchRootGrant{Label: want, Path: auth.ProjectDir}, true
	}
	return WatchRootGrant{}, false
}

func formatGitRefState(oid string, exists bool) string {
	if !exists || strings.TrimSpace(oid) == "" {
		return "missing"
	}
	return "oid:" + strings.TrimSpace(oid)
}

func parseGitRefState(state string) (oid string, exists bool) {
	if rest, ok := strings.CutPrefix(strings.TrimSpace(state), "oid:"); ok && rest != "" {
		return rest, true
	}
	return "", false
}

// NotifyWorkspaceChange reconciles live file_change and git_ref watches matching projectID or rootDir.
func (s *Store) NotifyWorkspaceChange(ctx context.Context, projectID, rootDir string) error {
	if !s.Configured() {
		return nil
	}
	query := `SELECT ` + registrationColumns + ` FROM watch_registrations WHERE state = 'live' AND (conditions_json LIKE '%file_change%' OR conditions_json LIKE '%git_ref%')`
	var args []any
	if strings.TrimSpace(projectID) != "" {
		query += ` AND project_id = ?`
		args = append(args, strings.TrimSpace(projectID))
	}
	query += ` ORDER BY seq ASC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	var regs []Registration
	for rows.Next() {
		reg, sErr := scanRegistration(rows)
		if sErr != nil {
			_ = rows.Close()
			return sErr
		}
		regs = append(regs, reg)
	}
	_ = rows.Close()

	cleanDir := ""
	if strings.TrimSpace(rootDir) != "" {
		if abs, err := filepath.Abs(rootDir); err == nil {
			cleanDir = filepath.Clean(abs)
		}
	}
	for _, reg := range regs {
		if cleanDir != "" && !regMatchesRootDir(reg, cleanDir) {
			continue
		}
		if _, err := reconcileSourceWatch(s, ctx, reg); err != nil {
			return err
		}
	}
	return nil
}

func regMatchesRootDir(reg Registration, cleanDir string) bool {
	for _, g := range reg.Authority.Roots {
		if g.Path != "" {
			if abs, err := filepath.Abs(g.Path); err == nil && filepath.Clean(abs) == cleanDir {
				return true
			}
		}
	}
	for _, p := range reg.Authority.RootPaths {
		if abs, err := filepath.Abs(p); err == nil && filepath.Clean(abs) == cleanDir {
			return true
		}
	}
	if reg.Authority.ProjectDir != "" {
		if abs, err := filepath.Abs(reg.Authority.ProjectDir); err == nil && filepath.Clean(abs) == cleanDir {
			return true
		}
	}
	return false
}

func reconcileSourceWatchByID(s *Store, ctx context.Context, watchID string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	reg, err := loadRegistrationByIDTx(ctx, tx, watchID)
	_ = tx.Rollback()
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if reg.Lifecycle.State != StateLive {
		s.sampler.unsubscribeWatch(reg.WatchID, s.heap)
		return false, nil
	}
	return reconcileSourceWatch(s, ctx, reg)
}

func reconcileSourceWatch(s *Store, ctx context.Context, reg Registration) (bool, error) {
	if allowed, reason := checkWatchAuthority(s, ctx, reg); !allowed {
		return true, revokeWatchAuthority(s, ctx, reg, "file_change", reason)
	}
	changed := false
	for altIdx, cond := range reg.Conditions {
		switch cond.Kind {
		case "git_ref":
			ok, err := reconcileGitRefAlternative(s, ctx, reg, altIdx, cond)
			if err != nil {
				return changed, err
			}
			if ok {
				changed = true
				if !reg.Lifecycle.Repeat {
					return true, nil
				}
			}
		case "file_change":
			ok, err := reconcileFileChangeAlternative(s, ctx, reg, altIdx, cond)
			if err != nil {
				return changed, err
			}
			if ok {
				changed = true
				if !reg.Lifecycle.Repeat {
					return true, nil
				}
			}
		}
	}
	return changed, nil
}

func reconcileGitRefAlternative(s *Store, ctx context.Context, reg Registration, altIdx int, cond awaitstore.Condition) (bool, error) {
	grant, ok := resolveWatchRootGrant(reg.Authority, cond.Root)
	if !ok || grant.Path == "" {
		_ = s.MarkSourceLost(ctx, reg.WatchID)
		return true, nil
	}
	oid, exists, err := git.ResolveFullRef(grant.Path, cond.Ref)
	if err != nil {
		_ = s.MarkSourceLost(ctx, reg.WatchID)
		return true, nil
	}
	curState := formatGitRefState(oid, exists)
	key := gitRefCursorKey(altIdx)
	prevState := reg.Cursors[key]
	if prevState == "" {
		_ = updateWatchCursor(s, ctx, reg.WatchID, key, curState)
		return false, nil
	}
	if curState == prevState {
		return false, nil
	}
	beforeOID, _ := parseGitRefState(prevState)
	occID := fmt.Sprintf("git_ref:%d:%s->%s", altIdx, prevState, curState)
	ev := map[string]any{
		"kind":       "git_ref",
		"root":       cond.Root,
		"ref":        cond.Ref,
		"before_oid": beforeOID,
		"after_oid":  oid,
		"deleted":    !exists,
	}
	_, recorded, err := s.RecordMatch(ctx, reg.WatchID, MatchInput{
		SourceKind:   "git_ref",
		OccurrenceID: occID,
		Outcome:      "satisfied",
		Evidence:     ev,
	}, map[string]string{key: curState})
	return recorded, err
}
