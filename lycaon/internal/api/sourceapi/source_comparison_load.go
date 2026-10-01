package sourceapi

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// comparisonFailure preserves rejection codes across reads and preparation.
type comparisonFailure struct {
	code    wire.ApiErrorCode
	message string
}

func (e *comparisonFailure) Error() string { return e.message }

func (s *Handler) writeComparisonError(w http.ResponseWriter, r *http.Request, err error) {
	var failure *comparisonFailure
	if errors.As(err, &failure) {
		s.responses.Fail(w, failure.code, failure.message)
		return
	}
	switch {
	case errors.Is(err, sourceledger.ErrHistoryNotFound), errors.Is(err, sourceledger.ErrPresentationNotFound), errors.Is(err, sourceledger.ErrBaselinePinNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeSourceHistoryNotFound, "Source history not found.")
	case errors.Is(err, sourceledger.ErrPresentationMismatch):
		s.responses.Fail(w, wire.ApiErrorCodeSourcePresentationEffectChanged, "This review has changed. Refresh the list and try again.")
	default:
		s.responses.InternalError(w, r, err)
	}
}

func (s *Handler) loadScopeComparison(ctx context.Context, p *project.Project, source wire.ScopeComparisonSource) (sourceledger.Comparison, error) {
	baseline, err := sourceledger.ParseBaseline(source.Baseline)
	if err != nil {
		return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "The baseline is not a source baseline."}
	}
	if baseline.Kind == sourceledger.BaselineCommit {
		return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "A commit comparison requires a folder and path."}
	}
	if start := source.PresentationAfterOrdinal; start != nil && (*start < 0 || baseline.Kind != sourceledger.BaselinePresentation) {
		return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "A presentation start requires a nonnegative ordinal and the presentation baseline."}
	}
	_, branch, err := s.workspaceSourceHead(ctx, p, source.FileID)
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		return sourceledger.Comparison{}, nil
	}
	if err != nil {
		return sourceledger.Comparison{}, err
	}
	return s.SourceLedger.CompareScope(ctx, p.ID, branch, baseline, source.FileID, sourceledger.ScopeComparisonOptions{
		UnmarkUserEdits:          source.MarkUserEdits != nil && !*source.MarkUserEdits,
		PresentationAfterOrdinal: source.PresentationAfterOrdinal,
	})
}

func (s *Handler) loadTurnComparison(ctx context.Context, p *project.Project, source wire.TurnComparisonSource) (sourceledger.Comparison, error) {
	if source.FileID == "" || source.SessionID == "" || source.Turn < 1 {
		return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "A turn comparison requires a file, a chat, and a positive turn."}
	}
	if _, _, err := s.workspaceSourceHead(ctx, p, source.FileID); err != nil {
		if errors.Is(err, sourceledger.ErrHistoryNotFound) {
			return sourceledger.Comparison{}, nil
		}
		return sourceledger.Comparison{}, err
	}
	return s.SourceLedger.CompareTurn(ctx, p.ID, source.SessionID, source.Turn, source.FileID, sourceledger.ScopeComparisonOptions{
		UnmarkUserEdits: source.MarkUserEdits != nil && !*source.MarkUserEdits,
	})
}

func (s *Handler) loadReviewedComparison(ctx context.Context, p *project.Project, source wire.ReviewedComparisonSource) (sourceledger.Comparison, error) {
	if source.FileID == "" || source.ReviewedThroughOrdinal <= 0 {
		return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "A reviewed comparison requires a file and a positive reviewed ordinal."}
	}
	if _, _, err := s.workspaceSourceHead(ctx, p, source.FileID); err != nil {
		return sourceledger.Comparison{}, err
	}
	return s.SourceLedger.CompareReviewed(ctx, p.ID, source.FileID, source.ReviewedThroughOrdinal)
}

// commitComparisonHead returns an empty head for a repository without commits.
func commitComparisonHead(ctx context.Context, mgr git.GitManager, rootAbs string) (string, error) {
	head, err := mgr.HeadSHA(ctx, rootAbs)
	if err == nil {
		return head, nil
	}
	// Status distinguishes an unborn branch from a Git read failure.
	status, statusErr := mgr.CommitStatus(ctx, rootAbs)
	if statusErr != nil {
		return "", statusErr
	}
	return status.Head, nil
}

func (s *Handler) loadCommitComparison(ctx context.Context, p *project.Project, source wire.CommitComparisonSource, trees *commitTrees) (sourceledger.Comparison, error) {
	if source.RootID == "" || !filepath.IsLocal(source.Path) || source.Path == "." {
		return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "A commit comparison requires a folder and a relative file path."}
	}
	rootAbs, _, mapped := s.resolveRootRepoPosition(ctx, p, source.RootID)
	mgr := s.Git.Manager()
	if !mapped {
		return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeSourceVersionUnavailable, "Git is unavailable for this folder."}
	}
	head, err := commitComparisonHead(ctx, mgr, rootAbs)
	if err != nil {
		return sourceledger.Comparison{}, err
	}
	if source.ExpectedHead != nil && *source.ExpectedHead != head {
		return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeSourceHistoryChanged, "Git HEAD changed. Refresh the comparison."}
	}
	oid, batched := trees.oid(source.RootID, head, source.Path)
	if !batched {
		read, err := commitPathOID(ctx, mgr, rootAbs, head, source.Path)
		if err != nil {
			return sourceledger.Comparison{}, err
		}
		oid = read
	}
	before := gitBlobComparisonSide(ctx, mgr, rootAbs, oid)
	before.RootID, before.Path = source.RootID, source.Path
	after := readWorkingCommit(p, source.RootID, rootAbs, source.Path).Side
	op := wire.SourceChangeOpWrite
	if after.State == "absent" {
		op = wire.SourceChangeOpDelete
	} else if before.State == "absent" {
		op = wire.SourceChangeOpCreate
	}
	return sourceledger.Comparison{InRange: true, Op: op, Before: before, After: after}, nil
}

func (s *Handler) loadBlobComparison(ctx context.Context, p *project.Project, source wire.BlobComparisonSource) (sourceledger.Comparison, error) {
	if source.RootID == "" {
		return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "A repository object comparison requires a folder."}
	}
	mgr := s.Git.Manager()
	rootAbs, _, mapped := s.resolveRootRepoPosition(ctx, p, source.RootID)
	if !mapped {
		return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeSourceVersionUnavailable, "No Git repository serves this folder."}
	}
	diff := sourceledger.Comparison{InRange: true, Op: wire.SourceChangeOpWrite,
		Before: gitBlobComparisonSide(ctx, mgr, rootAbs, source.BeforeBlobOid),
		After:  gitBlobComparisonSide(ctx, mgr, rootAbs, source.BlobOid)}
	for _, side := range []*sourceledger.ComparisonSide{&diff.Before, &diff.After} {
		side.RootID, side.Path = source.RootID, source.DisplayPath
	}
	if source.BeforeBlobOid == "" {
		diff.Op = wire.SourceChangeOpCreate
	}
	return diff, nil
}

func (s *Handler) writeSourceReviewedComparison(w http.ResponseWriter, r *http.Request, p *project.Project, fileID string) {
	q := r.URL.Query()
	through, err := strconv.ParseInt(q.Get("reviewed_through_ordinal"), 10, 64)
	if err != nil || through <= 0 || fileID == "" || q.Has("baseline") || q.Has("mark_user_edits") || q.Has("presentation_after_ordinal") {
		s.responses.InvalidQueryParam(w, "reviewed_through_ordinal", "must be positive, with file_id and without scope options")
		return
	}
	diff, err := s.loadReviewedComparison(r.Context(), p, wire.ReviewedComparisonSource{FileID: fileID, ReviewedThroughOrdinal: through})
	if err != nil {
		s.writeComparisonError(w, r, err)
		return
	}
	s.writeSourceComparison(w, r, p.ID, diff)
}

func presentationComparisonStart(r *http.Request, baseline sourceledger.Baseline) (*int64, error) {
	if !r.URL.Query().Has("presentation_after_ordinal") {
		return nil, nil
	}
	ordinal, err := strconv.ParseInt(r.URL.Query().Get("presentation_after_ordinal"), 10, 64)
	if err != nil || ordinal < 0 || baseline.Kind != sourceledger.BaselinePresentation {
		return nil, &httpio.QueryParameterError{Parameter: "presentation_after_ordinal", Reason: "requires a nonnegative ordinal and the presentation baseline"}
	}
	return &ordinal, nil
}
