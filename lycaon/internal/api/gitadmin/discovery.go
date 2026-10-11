package gitadmin

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const GitRepoNotFoundMsg = "That repository is no longer part of this project."

// HandleGitRepos returns the ordered repository set for a project.
func (s *Handler) HandleGitRepos(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	activeRootID := s.activeRootIDFromSession(r.Context(), sessionID)
	roots := project.RootRefsFrom(p)
	repos, err := s.LoadOrderedRepos(r.Context(), p, roots, activeRootID, sessionID)
	if err != nil {
		s.WriteGitReposLoadError(w, r, err)
		return
	}

	out := make([]wire.GitRepoEntry, 0, len(repos))
	activeRepoID := ""
	for _, repo := range repos {
		entry := wire.GitRepoEntry{
			RepoID:    repo.ID,
			Label:     repo.Label,
			RootIDs:   append([]string(nil), repo.RootIDs...),
			Available: repo.Available,
		}
		if repo.Available && repo.Toplevel != "" {
			s.populateGitRepoSummary(r.Context(), &entry, repo)
			if activeRepoID == "" {
				activeRepoID = repo.ID
			}
		}
		out = append(out, entry)
	}
	httpio.WriteJSON(w, http.StatusOK, wire.GitReposView{Repos: out, ActiveRepoID: activeRepoID})
}

// Discovery answers from cache: a repository without a snapshot is pending, and
// an aged one serves its values while it refreshes.
func (s *Handler) populateGitRepoSummary(ctx context.Context, entry *wire.GitRepoEntry, repo git.RepoRef) {
	if entry == nil || !repo.Available || repo.Toplevel == "" {
		if entry != nil && entry.Available {
			entry.StatusPending = true
		}
		return
	}
	cached, found, err := s.Board.StatusCache.PeekOrRevalidate(ctx, repo.Toplevel)
	if err != nil || !found || cached.Status == nil {
		entry.StatusPending = true
		return
	}
	status := cached.Status
	entry.Branch = status.Branch
	entry.HeadShort = status.HeadShort
	entry.Upstream = status.Upstream
	entry.Ahead = status.Ahead
	entry.Behind = status.Behind
	entry.Dirty = status.Dirty
	entry.StagedCount = status.StagedCount
	entry.UnstagedCount = status.UnstagedCount
	entry.ChangedCount = len(status.Files)
}

// requireGitRepoQuery resolves the {id}/{repo_id} path, viewed through the
// worktree of the chat the session_id lens query names.
func (s *Handler) requireGitRepoQuery(w http.ResponseWriter, r *http.Request) (git.RepoRef, *project.Project, bool) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return git.RepoRef{}, nil, false
	}
	repoID := strings.TrimSpace(chi.URLParam(r, "repo_id"))
	scope, err := requestscope.ResolveSessionProject(s.SessionStore, r.Context(), p, sessionID)
	if err != nil {
		s.WriteGitReposLoadError(w, r, err)
		return git.RepoRef{}, nil, false
	}
	repos, err := s.LoadOrderedRepos(r.Context(), p, scope.Roots, "", sessionID)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return git.RepoRef{}, nil, false
	}
	for _, repo := range repos {
		if repo.ID == repoID && repo.Available {
			return repo, scope.Project, true
		}
	}
	s.responses.Fail(w, wire.ApiErrorCodeGitRepoNotFound, GitRepoNotFoundMsg)
	return git.RepoRef{}, nil, false
}

func (s *Handler) LoadOrderedRepos(ctx context.Context, p *project.Project, roots []projectroot.RootRef, activeRootID, sessionID string) ([]git.RepoRef, error) {
	sessionID = strings.TrimSpace(sessionID)
	sessionSubstituted := false
	var bindingRepoID string
	var bindingPath string
	if sessionID != "" {
		scope, err := requestscope.ResolveSessionProject(s.SessionStore, ctx, p, sessionID)
		if err != nil {
			return nil, err
		}
		roots = scope.Roots
		if scope.Binding != nil {
			sessionSubstituted = true
			bindingRepoID = scope.Binding.RepoID
			bindingPath = canonicalGitPath(scope.Binding.WorktreePath)
		}
	}
	// The cached set describes the project's own folders. A session bound to
	// a linked worktree substitutes that worktree for the bound repository's
	// top level; the worktree is its own top level, so no discovery is needed.
	var repos []git.RepoRef
	if s.RepoSetCache != nil {
		repos = s.RepoSetCache.GetOrLoad(ctx, p.ID, p.RootsGeneration, project.RootRefsFrom(p))
	} else {
		repos = git.DiscoverRepos(ctx, project.RootRefsFrom(p))
	}
	if sessionSubstituted {
		repos = substituteWorktreeRepo(repos, bindingRepoID, bindingPath)
	}
	primaryID := ""
	if prim, err := projectroot.PrimaryRoot(roots); err == nil {
		primaryID = prim.ID
	}
	return git.OrderRepos(repos, activeRootID, primaryID), nil
}

// substituteWorktreeRepo re-addresses the bound repository at its linked
// worktree. The worktree is its own top level, so the cached set needs no
// discovery, only the one substitution the binding names.
func substituteWorktreeRepo(repos []git.RepoRef, bindingRepoID, worktreePath string) []git.RepoRef {
	out := make([]git.RepoRef, 0, len(repos)+1)
	substituted := false
	for _, repo := range repos {
		copied := repo
		copied.RootIDs = append([]string(nil), repo.RootIDs...)
		if repo.Available && repo.ID == bindingRepoID {
			copied.Toplevel = worktreePath
			substituted = true
		}
		out = append(out, copied)
	}
	if !substituted && bindingRepoID != "" && worktreePath != "" {
		out = append(out, git.RepoRef{ID: bindingRepoID, Toplevel: worktreePath, Available: true})
	}
	return out
}

func canonicalGitPath(path string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	return filepath.Clean(path)
}

func (s *Handler) WriteGitReposLoadError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, sessionscope.ErrWorktreeStale) {
		s.responses.Fail(w, wire.ApiErrorCodeWorktreeStale, "This chat's worktree is missing. Use Return to project folder in the Git tab to continue.")
		return
	}
	if errors.Is(err, store.ErrSessionNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "session not found")
		return
	}
	s.responses.InternalError(w, r, err)
}

func (s *Handler) activeRootIDFromSession(ctx context.Context, sessionID string) string {
	if sessionID == "" {
		return ""
	}
	sess, err := s.SessionStore.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return ""
	}
	return strings.TrimSpace(sess.WorkspaceRootID)
}
