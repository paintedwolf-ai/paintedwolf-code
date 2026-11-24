package gitadmin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/project/worktrees"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleDeleteGitWorktree(w http.ResponseWriter, r *http.Request) {
	deleteSessionWorktree(s, w, r)
}

func deleteSessionWorktree(s *Handler, w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(chi.URLParam(r, "id"))
	mgr := s.git
	unlock, ok := s.acquireIdleMutation(w, sessionID)
	if !ok {
		return
	}
	defer unlock()
	if s.Sessions.Runner.Execution.HasRunningProcess(sessionID) {
		s.responses.FailDetails(w, wire.ApiErrorCodeSessionNotIdle, map[string]any{
			"blocker":          "active_work",
			"actor_session_id": sessionID,
		}, "This chat has a running command or terminal process — stop it first.")
		return
	}

	scope, ok := s.resolveWorktreeScope(w, r, sessionID)
	if !ok {
		return
	}
	if !scope.bound || scope.binding == nil {
		s.responses.Fail(w, wire.ApiErrorCodeWorktreeNotBound, "This chat has no worktree.")
		return
	}
	b := scope.binding

	if !isManagedSessionWorktreePath(scope.dataRoot, b.WorktreePath) {
		s.responses.FailDetails(w, wire.ApiErrorCodeWorktreeStale, map[string]any{
			"blocker": "unmanaged_worktree",
			"repo_id": b.RepoID,
			"path":    b.WorktreePath,
		}, "Only worktrees created by Painted Wolf Code can be removed from here.")
		return
	}

	if s.MutationGate != nil {
		if err := s.MutationGate.BeginMutation(scope.project.ID); err != nil {
			s.responses.FailDetails(w, wire.ApiErrorCodeProjectBusy, map[string]any{
				"blocker": "pending_save",
				"repo_id": b.RepoID,
			}, "A file operation is still running in this project. Wait for it to finish and try again.")
			return
		}
		defer s.MutationGate.EndMutation(scope.project.ID)
		if !s.MutationGate.MutationDrained(scope.project.ID) {
			s.responses.FailDetails(w, wire.ApiErrorCodeProjectBusy, map[string]any{
				"blocker":     "pending_save",
				"repo_id":     b.RepoID,
				"worktree_id": b.WorktreeID,
			}, "A file save is still finishing in this project. Wait for it to finish before returning to the project folder.")
			return
		}
	}

	checkoutIDs := collectBindingCheckoutIDs(r.Context(), s, scope.project.ID, b)
	primaryCheckoutID := strings.TrimSpace(b.WorktreeID)
	if primaryCheckoutID == "" && len(checkoutIDs) > 0 {
		primaryCheckoutID = checkoutIDs[0]
	}

	releaseRelated, blockedActor, blockedReason := acquireRelatedCheckoutSessionLocks(r.Context(), s, sessionID, primaryCheckoutID, checkoutIDs)
	if blockedReason != "" {
		s.responses.FailDetails(w, wire.ApiErrorCodeSessionNotIdle, map[string]any{
			"blocker":          "active_work",
			"repo_id":          b.RepoID,
			"worktree_id":      primaryCheckoutID,
			"actor_session_id": blockedActor,
		}, blockedReason)
		return
	}
	defer releaseRelated()

	if otherSessionID := findOtherBoundSession(r.Context(), s, sessionID, checkoutIDs); otherSessionID != "" {
		s.responses.FailDetails(w, wire.ApiErrorCodeSessionNotIdle, map[string]any{
			"blocker":          "bound_elsewhere",
			"repo_id":          b.RepoID,
			"worktree_id":      primaryCheckoutID,
			"actor_session_id": otherSessionID,
		}, "Another chat is bound to this worktree.")
		return
	}

	if handled, err := writeCheckoutEditorBlocker(r.Context(), s, w, scope.project.ID, b.RepoID, checkoutIDs); err != nil {
		s.responses.InternalError(w, r, err)
		return
	} else if handled {
		return
	}

	if !applyWorktreeDiskRemoval(s, w, r, mgr, scope.project, b, primaryCheckoutID) {
		return
	}

	if err := s.SessionStore.RetireWorktreeBinding(r.Context(), scope.sess.ID); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if s.Worktrees != nil {
		_, _ = s.Worktrees.Refresh(r.Context(), worktrees.RepoSpec{
			ProjectID: scope.project.ID,
			RepoID:    b.RepoID,
			Toplevel:  b.Toplevel,
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

func applyWorktreeDiskRemoval(
	s *Handler,
	w http.ResponseWriter,
	r *http.Request,
	mgr git.Manager,
	proj *project.Project,
	b *store.WorktreeBinding,
	primaryCheckoutID string,
) bool {
	_, statErr := os.Lstat(b.WorktreePath)
	pathExists := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		s.responses.InternalError(w, r, statErr)
		return false
	}

	if pathExists && s.Worktrees != nil && strings.TrimSpace(b.WorktreeID) != "" {
		if _, _, errCode, _, okLive := resolveCheckoutProject(s, r.Context(), proj, b.WorktreeID); !okLive &&
			(errCode == wire.ApiErrorCodeWorktreeUnavailable || errCode == wire.ApiErrorCodeWorktreeNotFound) {
			s.responses.FailDetails(w, wire.ApiErrorCodeWorktreeStale, map[string]any{
				"blocker":     "incarnation_mismatch",
				"repo_id":     b.RepoID,
				"worktree_id": b.WorktreeID,
				"path":        b.WorktreePath,
			}, "The directory at this worktree path was replaced outside Painted Wolf Code and will not be deleted automatically.")
			return false
		}
	}

	ready := pathExists && mgr.ValidateWorktree(r.Context(), b.Toplevel, b.WorktreePath, b.Branch) == nil
	if ready {
		st, err := mgr.Status(r.Context(), b.WorktreePath)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return false
		}
		if st.Dirty {
			failDirtyWorktree(s, w, r.Context(), proj, b, primaryCheckoutID, st.Files)
			return false
		}
		return runGuardedWorktreeAdminOp(
			s, w, r, mgr, b.Toplevel,
			"repository config changed during worktree remove",
			func() error { return mgr.RemoveWorktree(r.Context(), b.Toplevel, b.WorktreePath) },
		)
	}
	if !pathExists {
		return runGuardedWorktreeAdminOp(
			s, w, r, mgr, b.Toplevel,
			"repository config changed during stale worktree registration cleanup",
			func() error { return mgr.RemoveStaleWorktreeRegistration(r.Context(), b.Toplevel, b.WorktreePath) },
		)
	}
	s.responses.Fail(w, wire.ApiErrorCodeWorktreeStale, "This chat's worktree is invalid. Repair or remove the checkout at the shown path before returning to the project folder.")
	return false
}

func runGuardedWorktreeAdminOp(
	s *Handler,
	w http.ResponseWriter,
	r *http.Request,
	mgr git.Manager,
	toplevel string,
	driftMsg string,
	op func() error,
) bool {
	before, err := mgr.RepoConfigFingerprint(r.Context(), toplevel)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return false
	}
	if err := op(); err != nil {
		s.responses.InternalError(w, r, err)
		return false
	}
	after, err := mgr.RepoConfigFingerprint(r.Context(), toplevel)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return false
	}
	if after != before {
		s.responses.InternalError(w, r, errors.New(driftMsg))
		return false
	}
	return true
}

func failDirtyWorktree(
	s *Handler,
	w http.ResponseWriter,
	ctx context.Context,
	proj *project.Project,
	b *store.WorktreeBinding,
	primaryCheckoutID string,
	files []git.FileStatus,
) {
	const msg = "The worktree has uncommitted changes. Commit or discard them first."
	if len(files) == 0 {
		s.responses.FailDetails(w, wire.ApiErrorCodeWorktreeDirty, map[string]any{
			"blocker":       "dirty_worktree",
			"repo_id":       b.RepoID,
			"worktree_id":   primaryCheckoutID,
			"changed_count": 0,
		}, msg)
		return
	}
	roots := proj.SourceRoots()
	if s.Worktrees != nil && primaryCheckoutID != "" {
		if projected, _, _, _, okProj := s.ResolveCheckoutProject(ctx, proj, primaryCheckoutID); okProj {
			roots = projected.SourceRoots()
		}
	}
	rootID, rootRel := attributeChangedFile(b.WorktreePath, files[0].Path, roots)
	if rootID != "" {
		s.responses.FailDetails(w, wire.ApiErrorCodeWorktreeDirty, map[string]any{
			"blocker":       "dirty_worktree",
			"repo_id":       b.RepoID,
			"worktree_id":   primaryCheckoutID,
			"changed_count": len(files),
			"root_id":       rootID,
			"path":          rootRel,
		}, msg)
		return
	}
	s.responses.FailDetails(w, wire.ApiErrorCodeWorktreeDirty, map[string]any{
		"blocker":       "dirty_worktree",
		"repo_id":       b.RepoID,
		"worktree_id":   primaryCheckoutID,
		"changed_count": len(files),
		"path":          files[0].Path,
	}, msg)
}

func isManagedSessionWorktreePath(dataRoot, worktreePath string) bool {
	dataRoot = strings.TrimSpace(dataRoot)
	worktreePath = strings.TrimSpace(worktreePath)
	if worktreePath == "" {
		return false
	}
	if dataRoot == "" {
		return true
	}
	managedRoot := enginepaths.SessionWorktreesRootUnder(dataRoot)
	cleanRoot := filepath.Clean(managedRoot)
	cleanTarget := filepath.Clean(worktreePath)
	if rel, err := filepath.Rel(cleanRoot, cleanTarget); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true
	}
	canonRoot := gitrepo.CanonicalDir(managedRoot)
	canonParent := gitrepo.CanonicalDir(filepath.Dir(cleanTarget))
	if canonRoot != "" && canonParent != "" {
		canonTarget := filepath.Join(canonParent, filepath.Base(cleanTarget))
		if rel, err := filepath.Rel(canonRoot, canonTarget); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func collectBindingCheckoutIDs(ctx context.Context, s *Handler, projectID string, b *store.WorktreeBinding) []string {
	seen := make(map[string]struct{})
	var ids []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	add(b.WorktreeID)
	if s.Worktrees != nil {
		if items, err := s.Worktrees.Refresh(ctx, worktrees.RepoSpec{
			ProjectID: projectID,
			RepoID:    b.RepoID,
			Toplevel:  b.Toplevel,
		}); err == nil {
			targetCanon := gitrepo.CanonicalDir(b.WorktreePath)
			targetClean := filepath.Clean(b.WorktreePath)
			for _, item := range items {
				if item.WorktreePath == targetClean || (targetCanon != "" && gitrepo.CanonicalDir(item.WorktreePath) == targetCanon) {
					add(item.ID)
				}
			}
		}
	}
	if s.Database != nil {
		for _, id := range queryWorktreeIDsByPath(ctx, s.Database, projectID, b.RepoID, filepath.Clean(b.WorktreePath)) {
			add(id)
		}
	}
	return ids
}

func queryWorktreeIDsByPath(ctx context.Context, db *sql.DB, projectID, repoID, cleanPath string) []string {
	rows, err := db.QueryContext(ctx,
		`SELECT id FROM source_worktrees WHERE project_id = ? AND repo_id = ? AND worktree_path = ?`,
		projectID, repoID, cleanPath)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	return ids
}

func acquireRelatedCheckoutSessionLocks(
	ctx context.Context,
	s *Handler,
	rootSessionID string,
	primaryCheckoutID string,
	checkoutIDs []string,
) (func(), string, string) {
	if s.Database == nil || s.Sessions == nil {
		return func() {}, "", ""
	}
	seen := map[string]struct{}{rootSessionID: {}}
	var related []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		related = append(related, id)
	}

	for _, id := range querySessionDescendants(ctx, s.Database, rootSessionID) {
		add(id)
	}
	for _, wid := range checkoutIDs {
		for _, id := range queryCheckoutUsageActors(ctx, s.Database, wid) {
			add(id)
		}
	}

	slices.Sort(related)
	var unlocks []func()
	releaseAll := func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}
	for _, id := range related {
		unlock, ok := s.Sessions.Runner.Execution.TryIdleMutation(id)
		if !ok {
			releaseAll()
			return nil, id, "Active work is still running in this worktree — wait for it to finish or stop it first."
		}
		unlocks = append(unlocks, unlock)
		if s.Sessions.Runner.Execution.HasRunningProcess(id) {
			releaseAll()
			_ = primaryCheckoutID
			return nil, id, "A background process is still running in this worktree — stop it first."
		}
	}
	return releaseAll, "", ""
}

func querySessionDescendants(ctx context.Context, db *sql.DB, rootSessionID string) []string {
	rows, err := db.QueryContext(ctx, `
		WITH RECURSIVE descendants(id) AS (
			SELECT id FROM sessions WHERE parent_session_id = ?
			UNION ALL
			SELECT child.id FROM sessions child JOIN descendants d ON child.parent_session_id = d.id
		)
		SELECT id FROM descendants`, rootSessionID)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	return ids
}

func queryCheckoutUsageActors(ctx context.Context, db *sql.DB, worktreeID string) []string {
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT actor_session_id FROM source_worktree_usages WHERE worktree_id = ?`, worktreeID)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	return ids
}

func findOtherBoundSession(ctx context.Context, s *Handler, currentSessionID string, checkoutIDs []string) string {
	if s.Database == nil || len(checkoutIDs) == 0 {
		return ""
	}
	for _, wid := range checkoutIDs {
		var otherID string
		err := s.Database.QueryRowContext(ctx,
			`SELECT session_id FROM session_worktrees WHERE worktree_id = ? AND session_id != ? LIMIT 1`,
			wid, currentSessionID).Scan(&otherID)
		if err == nil && strings.TrimSpace(otherID) != "" {
			return strings.TrimSpace(otherID)
		}
	}
	return ""
}

func writeCheckoutEditorBlocker(
	ctx context.Context,
	s *Handler,
	w http.ResponseWriter,
	projectID string,
	repoID string,
	checkoutIDs []string,
) (bool, error) {
	if s.Database == nil || len(checkoutIDs) == 0 {
		return false, nil
	}
	for _, wid := range checkoutIDs {
		branchID := "worktree:" + wid
		var mutDocID, mutRootID, mutPath, mutStatus string
		err := s.Database.QueryRowContext(ctx, `
			SELECT document_id, root_id, path, status
			FROM editor_mutations
			WHERE project_id = ? AND branch_id = ? AND status IN ('prepared', 'file_applied', 'conflict')
			ORDER BY created_at ASC LIMIT 1`, projectID, branchID).Scan(&mutDocID, &mutRootID, &mutPath, &mutStatus)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if err == nil {
			msg := "A file save is still finishing in this worktree (" + mutPath + "). Wait for it to finish before returning to the project folder."
			if mutStatus == "conflict" {
				msg = "A file save in " + mutPath + " has an unresolved conflict. Open the file in Files to resolve it before returning to the project folder."
			}
			s.responses.FailDetails(w, wire.ApiErrorCodeWorktreeDirty, map[string]any{
				"blocker":     "pending_save",
				"repo_id":     repoID,
				"worktree_id": wid,
				"root_id":     mutRootID,
				"path":        mutPath,
				"document_id": mutDocID,
			}, msg)
			return true, nil
		}

		var docID, docRootID, docPath string
		err = s.Database.QueryRowContext(ctx, `
			SELECT id, root_id, path
			FROM editor_documents
			WHERE project_id = ? AND branch_id = ? AND (dirty = 1 OR diverged = 1 OR held_agent_version_id != '')
			ORDER BY path ASC, id ASC LIMIT 1`, projectID, branchID).Scan(&docID, &docRootID, &docPath)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if err == nil {
			s.responses.FailDetails(w, wire.ApiErrorCodeWorktreeDirty, map[string]any{
				"blocker":     "unsaved_editor",
				"repo_id":     repoID,
				"worktree_id": wid,
				"root_id":     docRootID,
				"path":        docPath,
				"document_id": docID,
			}, "Save or discard unsaved editor changes in "+docPath+" before returning to the project folder.")
			return true, nil
		}
	}
	return false, nil
}
