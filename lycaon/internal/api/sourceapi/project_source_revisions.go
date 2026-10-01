package sourceapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// HandleResolveProjectSourceRevisions answers which commits a typed revision names in each root.
func (s *Handler) HandleResolveProjectSourceRevisions(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	spec, present, err := httpio.SingleQueryValue(r, "spec")
	if err == nil && (!present || !git.ValidRevisionSpec(strings.TrimSpace(spec))) {
		err = &httpio.QueryParameterError{Parameter: "spec", Reason: "must be one commit, branch, or range without spaces"}
	}
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	spec = strings.TrimSpace(spec)
	mgr := s.Git.Manager()
	roots, found := revisionRoots(p, strings.TrimSpace(r.URL.Query().Get("root_id")))
	if !found {
		s.responses.Fail(w, wire.ApiErrorCodeRootNotFound, "Folder not found.")
		return
	}
	out := wire.SourceRevisionsResponse{Comparisons: []wire.SourceRevisionComparison{}}
	for _, root := range roots {
		rootAbs, _, mapped := s.resolveRootRepoPosition(r.Context(), p, root.ID)
		if !mapped {
			continue
		}
		comparison, resolved, err := mgr.ResolveRevision(r.Context(), rootAbs, spec)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		if resolved {
			out.Comparisons = append(out.Comparisons, wire.SourceRevisionComparison{
				RootID: root.ID, Spec: comparison.Spec, Kind: string(comparison.Kind), Label: comparison.Label,
				BeforeCommit: comparison.Before, AfterCommit: comparison.After, Subject: comparison.Subject,
			})
		}
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

// revisionRoots orders the primary root first; found is false only for an unknown root_id.
func revisionRoots(p *project.Project, rootID string) ([]projectroot.RootRef, bool) {
	all := project.RootRefsFrom(p)
	ordered := make([]projectroot.RootRef, 0, len(all))
	for _, root := range all {
		if rootID != "" && root.ID != rootID {
			continue
		}
		if root.IsPrimary {
			ordered = append([]projectroot.RootRef{root}, ordered...)
		} else {
			ordered = append(ordered, root)
		}
	}
	return ordered, rootID == "" || len(ordered) > 0
}

func (s *Handler) HandleGetProjectSourceRevisionReview(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	query := r.URL.Query()
	rootID := strings.TrimSpace(query.Get("root_id"))
	before, after := strings.TrimSpace(query.Get("from_revision")), strings.TrimSpace(query.Get("to_revision"))
	input, ok := s.readReviewPageInput(w, r, revisionReviewPages, rootID, before, after)
	if !ok {
		return
	}
	switch {
	case uuid.Validate(rootID) != nil:
		s.responses.InvalidQueryParam(w, "root_id", "must be a UUID")
		return
	case !git.FullObjectID(after):
		s.responses.InvalidQueryParam(w, "to_revision", "must be a full commit id")
		return
	case before != "" && !git.FullObjectID(before):
		s.responses.InvalidQueryParam(w, "from_revision", "must be a full commit id")
		return
	}
	req, err := s.loadRevisionReview(r.Context(), p, rootID, before, after)
	if err != nil {
		s.writeComparisonError(w, r, err)
		return
	}
	page, nextCursor, ok := s.readReviewPage(w, r, revisionReviewPages, req, input)
	if !ok {
		return
	}
	out := wire.SourceRevisionReview{
		RootID: req.rootID, BeforeCommit: req.opts.Before, AfterCommit: req.opts.After, Files: mapGitReviewFiles(page.Files),
		FilesTotal: page.Total, Insertions: page.Insertions, Deletions: page.Deletions, NextCursor: nextCursor,
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

// loadRevisionReview addresses two full commit ids in one root's repository;
// nothing is recorded.
func (s *Handler) loadRevisionReview(ctx context.Context, p *project.Project, rootID, before, after string) (sourceGitReviewRequest, error) {
	var req sourceGitReviewRequest
	mgr := s.Git.Manager()
	rootAbs, _, mapped := s.resolveRootRepoPosition(ctx, p, rootID)
	if !mapped {
		return req, &comparisonFailure{wire.ApiErrorCodeSourceVersionUnavailable, "The repository for this folder is unavailable."}
	}
	req.mgr, req.rootID, req.rootAbs = mgr, rootID, rootAbs
	req.opts = git.CommitReviewOptions{Before: before, After: after, Limit: 100}
	return req, nil
}

func (s *Handler) loadGitRangeComparison(ctx context.Context, p *project.Project, source wire.GitRangeComparisonSource) (sourceledger.Comparison, error) {
	if err := gitReviewPathFailure(source.Path); err != nil {
		return sourceledger.Comparison{}, err
	}
	req, err := s.loadRevisionReview(ctx, p, source.RootID, source.BeforeCommit, source.AfterCommit)
	if err != nil {
		return sourceledger.Comparison{}, err
	}
	return loadGitReviewFile(ctx, req, source.Path)
}
