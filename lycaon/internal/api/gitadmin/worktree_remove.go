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
	"github.com/lycaon/lycaon/internal/gitrepo"
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

	if blocker, found, err := findCheckoutEditorBlocker(r.Context(), s, scope.project.ID, b.RepoID, checkoutIDs); err != nil {
		s.responses.InternalError(w, r, err)
		return
	} else if found {
		s.responses.FailDetails(w, wire.ApiErrorCodeWorktreeDirty, blocker.details, blocker.message)
		return
	}

	_, statErr := os.Lstat(b.WorktreePath)
	pathExists := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		s.responses.InternalError(w, r, statErr)
		return
	}

	if pathExists && s.Worktrees != nil && strings.TrimSpace(b.WorktreeID) != "" {
		if _, _, errCode, _, okLive := resolveCheckoutProject(s, r.Context(), scope.project, b.WorktreeID); !okLive &&
			(errCode == wire.ApiErrorCodeWorktreeUnavailable || errCode == wire.ApiErrorCodeWorktreeNotFound) {
			s.responses.FailDetails(w, wire.ApiErrorCodeWorktreeStale, map[string]any{
				"blocker":     "incarnation_mismatch",
				"repo_id":     b.RepoID,
				"worktree_id": b.WorktreeID,
				"path":        b.WorktreePath,
			}, "The directory at this worktree path was replaced outside Painted Wolf Code and will not be deleted automatically.")
			return
		}
	}

	ready := pathExists && mgr.ValidateWorktree(r.Context(), b.Toplevel, b.WorktreePath, b.Branch) == nil
	if ready {
		st, err := mgr.Status(r.Context(), b.WorktreePath)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		if st.Dirty {
			details := map[string]any{
				"blocker":       "dirty_worktree",
				"repo_id":       b.RepoID,
				"worktree_id":   primaryCheckoutID,
				"changed_count": len(st.Files),
			}
			if len(st.Files) > 0 {
				roots := scope.project.SourceRoots()
				if s.Worktrees != nil && primaryCheckoutID != "" {
					if projected, _, _, _, okProj := s.ResolveCheckoutProject(r.Context(), scope.project, primaryCheckoutID); okProj {
						roots = projected.SourceRoots()
					}
				}
				rootID, rootRel := attributeChangedFile(b.WorktreePath, st.Files[0].Path, roots)
				if rootID != "" {
					details["root_id"] = rootID
					details["path"] = rootRel
				} else {
					details["path"] = st.Files[0].Path
				}
			}
			s.responses.FailDetails(w, wire.ApiErrorCodeWorktreeDirty, details, "The worktree has uncommitted changes. Commit or discard them first.")
			return
		}
		before, err := mgr.RepoConfigFingerprint(r.Context(), b.Toplevel)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		if err := mgr.RemoveWorktree(r.Context(), b.Toplevel, b.WorktreePath); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		after, err := mgr.RepoConfigFingerprint(r.Context(), b.Toplevel)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		if after != before {
			s.responses.InternalError(w, r, errors.New("repository config changed during worktree remove"))
			return
		}
	} else if !pathExists {
		before, err := mgr.RepoConfigFingerprint(r.Context(), b.Toplevel)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		if err := mgr.RemoveStaleWorktreeRegistration(r.Context(), b.Toplevel, b.WorktreePath); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		after, err := mgr.RepoConfigFingerprint(r.Context(), b.Toplevel)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		if after != before {
			s.responses.InternalError(w, r, errors.New("repository config changed during stale worktree registration cleanup"))
			return
		}
	} else {
		s.responses.Fail(w, wire.ApiErrorCodeWorktreeStale, "This chat's worktree is invalid. Repair or remove the checkout at the shown path before returning to the project folder.")
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
		rows, err := s.Database.QueryContext(ctx,
			`SELECT id FROM source_worktrees WHERE project_id = ? AND repo_id = ? AND worktree_path = ?`,
			projectID, b.RepoID, filepath.Clean(b.WorktreePath))
		if err == nil {
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var id string
				if scanErr := rows.Scan(&id); scanErr == nil {
					add(id)
				}
			}
		}
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

	descRows, err := s.Database.QueryContext(ctx, `
		WITH RECURSIVE descendants(id) AS (
			SELECT id FROM sessions WHERE parent_session_id = ?
			UNION ALL
			SELECT child.id FROM sessions child JOIN descendants d ON child.parent_session_id = d.id
		)
		SELECT id FROM descendants`, rootSessionID)
	if err == nil {
		for descRows.Next() {
			var id string
			if descRows.Scan(&id) == nil {
				add(id)
			}
		}
		_ = descRows.Close()
	}

	for _, wid := range checkoutIDs {
		uRows, uErr := s.Database.QueryContext(ctx,
			`SELECT DISTINCT actor_session_id FROM source_worktree_usages WHERE worktree_id = ?`, wid)
		if uErr == nil {
			for uRows.Next() {
				var id string
				if uRows.Scan(&id) == nil {
					add(id)
				}
			}
			_ = uRows.Close()
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

type checkoutEditorBlocker struct {
	details map[string]any
	message string
}

func findCheckoutEditorBlocker(ctx context.Context, s *Handler, projectID, repoID string, checkoutIDs []string) (checkoutEditorBlocker, bool, error) {
	if s.Database == nil || len(checkoutIDs) == 0 {
		return checkoutEditorBlocker{}, false, nil
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
			return checkoutEditorBlocker{}, false, err
		}
		if err == nil {
			msg := "A file save is still finishing in this worktree (" + mutPath + "). Wait for it to finish before returning to the project folder."
			if mutStatus == "conflict" {
				msg = "A file save in " + mutPath + " has an unresolved conflict. Open the file in Files to resolve it before returning to the project folder."
			}
			return checkoutEditorBlocker{
				details: map[string]any{
					"blocker":     "pending_save",
					"repo_id":     repoID,
					"worktree_id": wid,
					"root_id":     mutRootID,
					"path":        mutPath,
					"document_id": mutDocID,
				},
				message: msg,
			}, true, nil
		}

		var docID, docRootID, docPath string
		err = s.Database.QueryRowContext(ctx, `
			SELECT id, root_id, path
			FROM editor_documents
			WHERE project_id = ? AND branch_id = ? AND (dirty = 1 OR diverged = 1 OR held_agent_version_id != '')
			ORDER BY path ASC, id ASC LIMIT 1`, projectID, branchID).Scan(&docID, &docRootID, &docPath)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return checkoutEditorBlocker{}, false, err
		}
		if err == nil {
			return checkoutEditorBlocker{
				details: map[string]any{
					"blocker":     "unsaved_editor",
					"repo_id":     repoID,
					"worktree_id": wid,
					"root_id":     docRootID,
					"path":        docPath,
					"document_id": docID,
				},
				message: "Save or discard unsaved editor changes in " + docPath + " before returning to the project folder.",
			}, true, nil
		}
	}
	return checkoutEditorBlocker{}, false, nil
}
