package gitadmin

import (
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/project/worktrees"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// HandleGitWorktrees lists verified registered checkouts for one project repository.
func (s *Handler) HandleGitWorktrees(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	repoID := strings.TrimSpace(chi.URLParam(r, "repo_id"))
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))

	var (
		rootSessionID string
		binding       *store.WorktreeBinding
	)
	if sessionID != "" {
		sess, err := s.SessionStore.Get(r.Context(), sessionID)
		if err != nil {
			s.WriteGitReposLoadError(w, r, err)
			return
		}
		if sess == nil || strings.TrimSpace(sess.ProjectID) != strings.TrimSpace(p.ID) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "session not found")
			return
		}
		rootSessionID = sess.ID
		if strings.TrimSpace(sess.ParentSessionID) != "" {
			rootSessionID = strings.TrimSpace(sess.ParentSessionID)
		}
		b, bound, err := s.SessionStore.GetWorktreeBinding(r.Context(), sessionID)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		if bound {
			binding = b
		}
	}

	var baseRepo git.RepoRef
	found := false
	for _, repo := range s.LoadOrderedRepos(r.Context(), p, p.SourceRoots(), "", nil) {
		if repo.ID == repoID && repo.Available && repo.Toplevel != "" {
			baseRepo = repo
			found = true
			break
		}
	}
	if !found {
		s.responses.Fail(w, wire.ApiErrorCodeGitRepoNotFound, GitRepoNotFoundMsg)
		return
	}
	if s.Worktrees == nil {
		httpio.WriteJSON(w, http.StatusOK, wire.GitWorktreesView{
			RepoID:    baseRepo.ID,
			Worktrees: []wire.GitWorktreeEntry{},
		})
		return
	}

	checkouts, err := s.Worktrees.Read(r.Context(), worktrees.RepoSpec{
		ProjectID: p.ID,
		RepoID:    baseRepo.ID,
		Toplevel:  canonicalGitPath(baseRepo.Toplevel),
	})
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}

	var usages map[string]worktrees.ChatCheckoutUsage
	if rootSessionID != "" {
		usages, err = s.Worktrees.SummarizeChatRepoUsages(r.Context(), p.ID, baseRepo.ID, rootSessionID)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	}

	entries := make([]wire.GitWorktreeEntry, 0, len(checkouts))
	projectCheckoutID := ""
	boundWorktreeID := ""
	if binding != nil && binding.RepoID == baseRepo.ID && binding.WorktreeID != "" {
		boundWorktreeID = binding.WorktreeID
	}

	for _, c := range checkouts {
		admitted := CheckoutAdmitted(c.WorktreePath)
		available := c.Available && admitted
		isProjectCheckout := c.IsProjectCheckout()
		if isProjectCheckout && projectCheckoutID == "" {
			projectCheckoutID = c.ID
		}
		boundToSession := binding != nil && binding.RepoID == baseRepo.ID &&
			(binding.WorktreeID == c.ID || (binding.WorktreeID == "" && canonicalGitPath(binding.WorktreePath) == canonicalGitPath(c.WorktreePath)))
		if boundToSession && boundWorktreeID == "" {
			boundWorktreeID = c.ID
		}

		headShort := c.HeadSHA
		if len(headShort) > 8 {
			headShort = headShort[:8]
		}
		usage := usages[c.ID]
		workerIDs := append([]string(nil), usage.WorkerIDs...)
		if workerIDs == nil {
			workerIDs = []string{}
		}
		lastActivity := ""
		if !usage.LastTS.IsZero() {
			lastActivity = usage.LastTS.UTC().Format(time.RFC3339Nano)
		}

		entry := wire.GitWorktreeEntry{
			WorktreeID:        c.ID,
			Kind:              string(c.Kind),
			Path:              c.WorktreePath,
			Branch:            c.Branch,
			BaseBranch:        c.BaseBranch,
			HeadSHA:           c.HeadSHA,
			HeadShort:         headShort,
			Detached:          c.Detached,
			Locked:            c.Locked,
			LockReason:        c.LockReason,
			Prunable:          c.Prunable,
			PruneReason:       c.PruneReason,
			Available:         available,
			Managed:           c.Managed,
			IsProjectCheckout: isProjectCheckout,
			BoundToSession:    boundToSession,
			CreatedByChat:     usage.CreatedByChat,
			UsedByChat:        usage.UsedByChat,
			WorkerIDs:         workerIDs,
			LastActivityAt:    lastActivity,
		}
		// Never launch status subprocesses for uncached checkouts when listing the collection.
		if available {
			if cached, ok := s.Board.StatusCache.Peek(c.WorktreePath); ok && cached.Status != nil {
				entry.StatusCached = true
				entry.Dirty = cached.Status.Dirty
				entry.StagedCount = cached.Status.StagedCount
				entry.UnstagedCount = cached.Status.UnstagedCount
				entry.ChangedCount = len(cached.Status.Files)
				entry.Ahead = cached.Status.Ahead
				entry.Behind = cached.Status.Behind
				if cached.Status.Branch != "" && !c.Detached {
					entry.Branch = cached.Status.Branch
				}
				if cached.Status.HeadShort != "" {
					entry.HeadShort = cached.Status.HeadShort
				}
			}
		}
		entries = append(entries, entry)
	}

	httpio.WriteJSON(w, http.StatusOK, wire.GitWorktreesView{
		RepoID:            baseRepo.ID,
		ProjectCheckoutID: projectCheckoutID,
		BoundWorktreeID:   boundWorktreeID,
		Worktrees:         entries,
	})
}

// HandleGitDiff returns staged, unstaged, or commit-preview patches and stats for a checkout.
func (s *Handler) HandleGitDiff(w http.ResponseWriter, r *http.Request) {
	target, ok := s.requireGitCheckoutTarget(w, r)
	if !ok {
		return
	}
	mode := strings.TrimSpace(r.URL.Query().Get("mode"))
	if mode == "" {
		mode = "unstaged"
	}
	if mode != "unstaged" && mode != "staged" && mode != "commit" {
		s.responses.InvalidQueryParam(w, "mode", "must be unstaged, staged, or commit")
		return
	}
	var paths []string
	rawPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if rawPath != "" {
		if !filepath.IsLocal(rawPath) || rawPath == "." {
			s.responses.InvalidQueryParam(w, "path", "must be a relative repository path")
			return
		}
		paths = []string{filepath.ToSlash(rawPath)}
	}

	mgr := s.git
	dir := target.Repo.Toplevel
	headSHA, _ := mgr.HeadSHA(r.Context(), dir)

	var (
		patchBuilder strings.Builder
		stats        []git.GitDiffStatEntry
	)

	switch mode {
	case "staged":
		opts := git.GitDiffOpts{Staged: true, Paths: paths}
		patch, err := mgr.Diff(r.Context(), dir, opts)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		patchBuilder.WriteString(patch)
		stats, _ = mgr.DiffStat(r.Context(), dir, opts)
	case "unstaged":
		opts := git.GitDiffOpts{Paths: paths}
		patch, err := mgr.Diff(r.Context(), dir, opts)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		patchBuilder.WriteString(patch)
		stats, _ = mgr.DiffStat(r.Context(), dir, opts)
		s.appendUntrackedDiffAndStats(r, mgr, dir, paths, &patchBuilder, &stats)
	case "commit":
		opts := git.GitDiffOpts{Paths: paths}
		if headSHA != "" {
			opts.BaseRef = "HEAD"
			patch, err := mgr.Diff(r.Context(), dir, opts)
			if err != nil {
				s.responses.InternalError(w, r, err)
				return
			}
			patchBuilder.WriteString(patch)
			stats, _ = mgr.DiffStat(r.Context(), dir, opts)
		} else {
			if staged, err := mgr.Diff(r.Context(), dir, git.GitDiffOpts{Staged: true, Paths: paths}); err == nil {
				patchBuilder.WriteString(staged)
			}
			if unstaged, err := mgr.Diff(r.Context(), dir, git.GitDiffOpts{Paths: paths}); err == nil && unstaged != "" {
				if patchBuilder.Len() > 0 {
					patchBuilder.WriteString("\n")
				}
				patchBuilder.WriteString(unstaged)
			}
			stats, _ = mgr.DiffStat(r.Context(), dir, git.GitDiffOpts{Staged: true, Paths: paths})
		}
		s.appendUntrackedDiffAndStats(r, mgr, dir, paths, &patchBuilder, &stats)
	}

	roots := target.ProjectedProject.SourceRoots()
	items := make([]wire.GitDiffStatItem, 0, len(stats))
	for _, st := range stats {
		rootID, rootRel := attributeChangedFile(dir, st.Path, roots)
		items = append(items, wire.GitDiffStatItem{
			Path:             st.Path,
			Insertions:       st.Insertions,
			Deletions:        st.Deletions,
			Untracked:        st.Untracked,
			RootID:           rootID,
			RootRelativePath: rootRel,
		})
	}

	httpio.WriteJSON(w, http.StatusOK, wire.GitDiffView{
		RepoID:     target.Repo.ID,
		WorktreeID: target.Checkout.ID,
		Mode:       mode,
		HeadSHA:    headSHA,
		Patch:      patchBuilder.String(),
		Files:      items,
	})
}

func (s *Handler) appendUntrackedDiffAndStats(
	r *http.Request,
	mgr *git.Manager,
	dir string,
	paths []string,
	patchBuilder *strings.Builder,
	stats *[]git.GitDiffStatEntry,
) {
	untrackedPaths, err := mgr.UntrackedPaths(r.Context(), dir, paths)
	if err != nil || len(untrackedPaths) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(*stats))
	for _, st := range *stats {
		seen[st.Path] = struct{}{}
	}
	for _, rel := range untrackedPaths {
		uDiff, dErr := mgr.UntrackedFileDiff(r.Context(), dir, rel)
		if dErr == nil && uDiff != "" {
			if patchBuilder.Len() > 0 && !strings.HasSuffix(patchBuilder.String(), "\n") {
				patchBuilder.WriteString("\n")
			}
			patchBuilder.WriteString(uDiff)
		}
		if _, dup := seen[rel]; !dup {
			seen[rel] = struct{}{}
			*stats = append(*stats, git.GitDiffStatEntry{Path: rel, Untracked: true})
		}
	}
}

// HandleGitStashes lists shared repository stashes.
func (s *Handler) HandleGitStashes(w http.ResponseWriter, r *http.Request) {
	target, ok := s.requireGitCheckoutTarget(w, r)
	if !ok {
		return
	}
	limit := 50
	if parsed, present, err := httpio.OptionalIntQuery(r, "limit", 1, 200); err != nil {
		s.responses.InvalidQuery(w, err)
		return
	} else if present {
		limit = parsed
	}
	entries, err := s.git.ListStashes(r.Context(), target.Repo.Toplevel, 0, limit)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	out := make([]wire.GitStashEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, wire.GitStashEntry{
			Ref:     entry.Ref,
			OID:     entry.OID,
			Subject: entry.Subject,
		})
	}
	httpio.WriteJSON(w, http.StatusOK, wire.GitStashesView{
		RepoID:     target.Repo.ID,
		WorktreeID: target.Checkout.ID,
		Stashes:    out,
	})
}
