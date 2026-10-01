package gitadmin

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	wire "github.com/lycaon/lycaon/pkg/api"
)

var gitChangesPages = pagecursor.For[int]("git_changes")

// HandleGitStatus returns the working-tree snapshot for the Den Git tab.
func (s *Handler) HandleGitStatus(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.requireGitRepoQuery(w, r)
	if !ok {
		return
	}
	summary, status := s.gitStatusSummary(r.Context(), repo)
	httpio.WriteJSON(w, status, summary)
}

// HandleGitChanges returns one cached changed-path page.
func (s *Handler) HandleGitChanges(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.requireGitRepoQuery(w, r)
	if !ok {
		return
	}
	limit := 200
	if parsed, present, err := httpio.OptionalIntQuery(r, "limit", 1, 500); err != nil {
		s.responses.InvalidQuery(w, err)
		return
	} else if present {
		limit = parsed
	}
	scope := pagecursor.Scope(repo.ID, repo.Toplevel)
	offset := 0
	expectedRevision := uint64(0)
	if parsed, present, err := httpio.OptionalUint64Query(r, "revision", 1); err != nil {
		s.responses.InvalidQuery(w, err)
		return
	} else if present {
		expectedRevision = parsed
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		cursorRevision, cursorOffset, err := gitChangesPages.DecodeAt(raw, scope, func(gen uint64) bool {
			return expectedRevision == 0 || gen == expectedRevision
		})
		if err != nil {
			s.responses.PageCursorError(w, r, "cursor", err)
			return
		}
		expectedRevision, offset = cursorRevision, cursorOffset
	}
	page, status, err := s.gitChangesPage(r.Context(), repo, project.RootRefsFrom(p), offset, limit, expectedRevision)
	if errors.Is(err, pagecursor.ErrExpired) {
		s.responses.PageCursorError(w, r, "cursor", pagecursor.ErrExpired)
		return
	}
	if err != nil {
		// Error responses must not contain a changes page.
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, status, page)
}

// gitStatusSummary maps the newest cached status generation. A repository with no
// snapshot answers 202; an aged one answers its values with refreshing set.
func (s *Handler) gitStatusSummary(ctx context.Context, repo git.RepoRef) (wire.GitStatusSummary, int) {
	rootIDs := append([]string(nil), repo.RootIDs...)
	if rootIDs == nil {
		rootIDs = []string{}
	}
	empty := wire.GitStatusSummary{
		Available: repo.Available,
		RepoID:    repo.ID,
		RootIDs:   rootIDs,
	}
	if !repo.Available || repo.Toplevel == "" {
		return empty, http.StatusOK
	}
	cached, found, err := s.Board.StatusCache.PeekOrRevalidate(ctx, repo.Toplevel)
	if err != nil || !found || cached.Status == nil {
		empty.Refreshing = true
		return empty, http.StatusAccepted
	}
	status := cached.Status
	return wire.GitStatusSummary{
		Available:     true,
		RepoID:        repo.ID,
		RootIDs:       rootIDs,
		Revision:      cached.Revision,
		Refreshing:    !cached.CacheHit,
		Branch:        status.Branch,
		HeadShort:     status.HeadShort,
		Upstream:      status.Upstream,
		Ahead:         status.Ahead,
		Behind:        status.Behind,
		Dirty:         status.Dirty,
		StagedCount:   status.StagedCount,
		UnstagedCount: status.UnstagedCount,
		ChangedCount:  len(status.Files),
	}, http.StatusOK
}

func (s *Handler) gitChangesPage(
	ctx context.Context,
	repo git.RepoRef,
	roots []projectroot.RootRef,
	offset int,
	limit int,
	expectedRevision uint64,
) (wire.GitChangesPage, int, error) {
	empty := wire.GitChangesPage{RepoID: repo.ID, Files: []wire.GitFileEntry{}}
	cached, found, err := s.Board.StatusCache.PeekOrRevalidate(ctx, repo.Toplevel)
	if err != nil {
		return empty, http.StatusInternalServerError, err
	}
	if !found || cached.Status == nil {
		empty.Refreshing = true
		return empty, http.StatusAccepted, nil
	}
	empty.Revision = cached.Revision
	if expectedRevision != 0 && cached.Revision != expectedRevision {
		return empty, http.StatusConflict, pagecursor.ErrExpired
	}
	empty.Refreshing = !cached.CacheHit
	if offset >= len(cached.Status.Files) {
		return empty, http.StatusOK, nil
	}
	end := min(offset+limit, len(cached.Status.Files))
	files := make([]wire.GitFileEntry, 0, end-offset)
	for _, file := range cached.Status.Files[offset:end] {
		rootID, rootRel := attributeChangedFile(repo.Toplevel, file.Path, roots)
		files = append(files, wire.GitFileEntry{
			Path: file.Path, Status: file.Status, RootID: rootID, RootRelativePath: rootRel,
		})
	}
	empty.Files = files
	if end < len(cached.Status.Files) {
		empty.NextCursor, err = gitChangesPages.EncodeAt(pagecursor.Scope(repo.ID, repo.Toplevel), cached.Revision, end)
		if err != nil {
			return empty, http.StatusInternalServerError, err
		}
	}
	return empty, http.StatusOK, nil
}

func (s *Handler) gitMutationResult(ctx context.Context, repo git.RepoRef) wire.GitMutationResult {
	result := wire.GitMutationResult{RepoID: repo.ID, Refreshing: true}
	if repo.Toplevel == "" {
		return result
	}
	s.Board.StatusCache.InvalidateWithSource(repo.Toplevel, repochange.SourceMutation)
	cached, found, _ := s.Board.StatusCache.PeekOrRevalidate(ctx, repo.Toplevel)
	if found {
		result.Revision = cached.Revision
	}
	return result
}

// attributeChangedFile picks the longest project root that contains the porcelain path.
func attributeChangedFile(toplevel, porcelainPath string, roots []projectroot.RootRef) (rootID, rootRel string) {
	abs := filepath.Clean(filepath.Join(toplevel, filepath.FromSlash(porcelainPath)))
	var (
		best    projectroot.RootRef
		bestLen int
		found   bool
		bestAbs string
	)
	for _, root := range roots {
		rootAbs, err := filepath.Abs(root.Path)
		if err != nil {
			continue
		}
		rootAbs = filepath.Clean(rootAbs)
		if !pathUnderRoot(abs, rootAbs) {
			continue
		}
		if !found || len(rootAbs) > bestLen {
			best = root
			bestLen = len(rootAbs)
			bestAbs = rootAbs
			found = true
		}
	}
	if !found {
		return "", ""
	}
	rel, err := filepath.Rel(bestAbs, abs)
	if err != nil {
		return best.ID, ""
	}
	return best.ID, filepath.ToSlash(rel)
}

func pathUnderRoot(abs, rootAbs string) bool {
	if abs == rootAbs {
		return true
	}
	prefix := rootAbs + string(filepath.Separator)
	return strings.HasPrefix(abs, prefix)
}
