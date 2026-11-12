package gitadmin

import (
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type worktreeScope struct {
	sess     *wire.Session
	project  *project.Project
	repos    []git.RepoRef
	binding  *store.WorktreeBinding
	bound    bool
	dataRoot string
}

func (s *Handler) resolveWorktreeScope(w http.ResponseWriter, r *http.Request, sessionID string) (worktreeScope, bool) {
	sess, ok := requestscope.Session(s.SessionStore, s.responses, w, r, sessionID)
	if !ok {
		return worktreeScope{}, false
	}
	p, ok := requestscope.ProjectByID(s.ProjectRegistry, s.responses, w, r, sess.ProjectID)
	if !ok {
		return worktreeScope{}, false
	}
	binding, bound, err := s.SessionStore.GetWorktreeBinding(r.Context(), sessionID)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return worktreeScope{}, false
	}
	roots := project.RootRefsFrom(p)
	// Project folders remain discoverable when a worktree binding is stale.
	repos, err := s.LoadOrderedRepos(r.Context(), p, roots, s.activeRootIDFromSession(r.Context(), sessionID), "")
	if err != nil {
		s.responses.InternalError(w, r, err)
		return worktreeScope{}, false
	}
	repos = reposInRoots(repos, roots)
	return worktreeScope{
		sess:     sess,
		project:  p,
		repos:    repos,
		binding:  binding,
		bound:    bound,
		dataRoot: strings.TrimSpace(s.DataDir),
	}, true
}

func (s *Handler) acquireIdleMutation(w http.ResponseWriter, sessionID string) (func(), bool) {
	unlock, ok := s.Sessions.Runner.Execution.TryIdleMutation(sessionID)
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeSessionNotIdle, "session has a turn in flight — stop it first")
		return nil, false
	}
	return unlock, true
}

// reposInRoots keeps repositories that hold one of roots, so a chat binds
// worktrees only where its agent works.
func reposInRoots(repos []git.RepoRef, roots []projectroot.RootRef) []git.RepoRef {
	ids := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		ids[root.ID] = struct{}{}
	}
	out := make([]git.RepoRef, 0, len(repos))
	for _, repo := range repos {
		for _, id := range repo.RootIDs {
			if _, ok := ids[id]; ok {
				out = append(out, repo)
				break
			}
		}
	}
	return out
}
