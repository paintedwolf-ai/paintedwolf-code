package sourceapi

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
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

func (s *Comparisons) writeComparisonError(w http.ResponseWriter, r *http.Request, err error) {
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

func (s *Comparisons) loadScopeComparison(ctx context.Context, p *project.Project, source wire.ScopeComparisonSource) (sourceledger.Comparison, error) {
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
	_, branch, err := s.Workspace.workspaceSourceHead(ctx, p, source.FileID)
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		return sourceledger.Comparison{}, nil
	}
	if err != nil {
		return sourceledger.Comparison{}, err
	}
	return s.SourceLedger.Comparisons.CompareScope(ctx, p.ID, branch, baseline, source.FileID, sourceledger.ScopeComparisonOptions{
		UnmarkUserEdits:          source.MarkUserEdits != nil && !*source.MarkUserEdits,
		PresentationAfterOrdinal: source.PresentationAfterOrdinal,
	})
}

func (s *Comparisons) loadTurnComparison(ctx context.Context, p *project.Project, source wire.TurnComparisonSource) (sourceledger.Comparison, error) {
	if source.FileID == "" || source.SessionID == "" || source.Turn < 1 {
		return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "A turn comparison requires a file, a chat, and a positive turn."}
	}
	if _, _, err := s.Workspace.workspaceSourceHead(ctx, p, source.FileID); err != nil {
		if errors.Is(err, sourceledger.ErrHistoryNotFound) {
			return sourceledger.Comparison{}, nil
		}
		return sourceledger.Comparison{}, err
	}
	return s.SourceLedger.Comparisons.CompareTurn(ctx, p.ID, source.SessionID, source.Turn, source.FileID, sourceledger.ScopeComparisonOptions{
		UnmarkUserEdits: source.MarkUserEdits != nil && !*source.MarkUserEdits,
	})
}

func (s *Comparisons) loadReviewedComparison(ctx context.Context, p *project.Project, source wire.ReviewedComparisonSource) (sourceledger.Comparison, error) {
	if source.FileID == "" || source.ReviewedThroughOrdinal <= 0 {
		return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "A reviewed comparison requires a file and a positive reviewed ordinal."}
	}
	if _, _, err := s.Workspace.workspaceSourceHead(ctx, p, source.FileID); err != nil {
		return sourceledger.Comparison{}, err
	}
	return s.SourceLedger.Comparisons.CompareReviewed(ctx, p.ID, source.FileID, source.ReviewedThroughOrdinal)
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

func (s *Comparisons) loadCommitComparison(ctx context.Context, p *project.Project, source wire.CommitComparisonSource, trees *commitTrees) (sourceledger.Comparison, error) {
	if source.RootID == "" || !filepath.IsLocal(source.Path) || source.Path == "." {
		return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "A commit comparison requires a folder and a relative file path."}
	}
	stage := strings.TrimSpace(source.Stage)
	if stage == "" {
		stage = "working"
	}
	if stage != "working" && stage != "staged" && stage != "unstaged" {
		return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "A commit comparison stage must be working, staged, or unstaged."}
	}
	targetProject := p
	var fallbackBeforeRootAbs string
	checkoutUnavailable := false
	if strings.TrimSpace(source.WorktreeID) != "" {
		baseProject := p
		if s.ProjectRegistry != nil {
			if loaded, err := s.ProjectRegistry.Get(ctx, p.ID); err == nil && loaded != nil {
				baseProject = loaded
			}
		}
		baseRootAbs, _, baseMapped := s.resolveRootRepoPosition(ctx, baseProject, source.RootID)
		if !baseMapped {
			return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeSourceVersionUnavailable, "Git is unavailable for this folder."}
		}
		baseRepo, ok := gitrepo.Discover(baseRootAbs)
		if !ok {
			return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeSourceVersionUnavailable, "Git is unavailable for this folder."}
		}
		projected, checkout, errCode, errMsg, resolved := s.Git.ResolveCheckoutProject(ctx, baseProject, source.WorktreeID)
		if !resolved {
			if source.ExpectedAfterSHA256 != nil && (errCode == wire.ApiErrorCodeWorktreeUnavailable || errCode == wire.ApiErrorCodeWorktreeNotFound) {
				checkoutUnavailable = true
				fallbackBeforeRootAbs = baseRootAbs
			} else {
				return sourceledger.Comparison{}, &comparisonFailure{errCode, errMsg}
			}
		} else {
			if gitrepo.CanonicalDir(checkout.Toplevel) != gitrepo.CanonicalDir(baseRepo.Root) {
				return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeWorktreeNotFound, "That checkout is not registered for this repository."}
			}
			rootRetained := false
			for _, r := range projected.Roots {
				if r.ID == source.RootID {
					rootRetained = true
					break
				}
			}
			if !rootRetained {
				if source.ExpectedAfterSHA256 != nil {
					checkoutUnavailable = true
					fallbackBeforeRootAbs = baseRootAbs
				} else {
					return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeWorktreeUnavailable, "That checkout is unavailable for this folder."}
				}
			} else {
				targetProject = projected
				trees = nil
			}
		}
	}
	mgr := s.Git.Manager()
	if checkoutUnavailable {
		beforePath := source.Path
		if bp := strings.TrimSpace(source.BeforePath); bp != "" {
			beforePath = bp
		}
		before := gitBlobComparisonSide(ctx, mgr, fallbackBeforeRootAbs, strings.TrimSpace(source.BeforeBlobOID))
		before.RootID, before.Path = source.RootID, beforePath
		after := unavailableWorkingSide(source.RootID, source.Path, source.ExpectedAfterSHA256)
		return finalizeCommitComparison(before, after), nil
	}
	rootAbs, prefix, mapped := s.resolveRootRepoPosition(ctx, targetProject, source.RootID)
	if !mapped {
		return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeSourceVersionUnavailable, "Git is unavailable for this folder."}
	}
	head, err := commitComparisonHead(ctx, mgr, rootAbs)
	if err != nil {
		return sourceledger.Comparison{}, err
	}
	if source.ExpectedHead != nil && *source.ExpectedHead != head && source.ExpectedAfterSHA256 == nil {
		return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeSourceHistoryChanged, "Git HEAD changed. Refresh the comparison."}
	}

	var before, after sourceledger.ComparisonSide
	relRepoPath := prefix + strings.TrimPrefix(filepath.ToSlash(source.Path), "/")

	switch stage {
	case "working":
		oid := strings.TrimSpace(source.BeforeBlobOID)
		if oid == "" {
			var batched bool
			oid, batched = trees.oid(source.RootID, head, source.Path)
			if !batched {
				read, err := commitPathOID(ctx, mgr, rootAbs, head, source.Path)
				if err != nil {
					return sourceledger.Comparison{}, err
				}
				oid = read
			}
		}
		before = gitBlobComparisonSide(ctx, mgr, rootAbs, oid)
		before.RootID, before.Path = source.RootID, source.Path
		after = verifyHistoricalWorkingSide(readWorkingCommit(targetProject, source.RootID, rootAbs, source.Path).Side, source.ExpectedAfterSHA256)
	case "staged":
		beforeLookupPath := source.Path
		if bp := strings.TrimSpace(source.BeforePath); bp != "" {
			if !filepath.IsLocal(bp) || bp == "." {
				return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "before_path must be a relative file path."}
			}
			beforeLookupPath = bp
		}
		headOID := strings.TrimSpace(source.BeforeBlobOID)
		if headOID == "" {
			var err error
			headOID, err = commitPathOID(ctx, mgr, rootAbs, head, beforeLookupPath)
			if err != nil {
				return sourceledger.Comparison{}, err
			}
		}
		concrete := s.Git.ConcreteManager()
		if concrete == nil {
			return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeSourceVersionUnavailable, "Git index inspection is unavailable."}
		}
		indexOID, err := concrete.IndexBlobOID(ctx, rootAbs, relRepoPath)
		if err != nil {
			return sourceledger.Comparison{}, err
		}
		before = gitBlobComparisonSide(ctx, mgr, rootAbs, headOID)
		before.RootID, before.Path = source.RootID, beforeLookupPath
		after = gitBlobComparisonSide(ctx, mgr, rootAbs, indexOID)
		after.RootID, after.Path = source.RootID, source.Path
	case "unstaged":
		beforePath := source.Path
		if bp := strings.TrimSpace(source.BeforePath); bp != "" {
			if !filepath.IsLocal(bp) || bp == "." {
				return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "before_path must be a relative file path."}
			}
			beforePath = bp
		}
		indexOID := strings.TrimSpace(source.BeforeBlobOID)
		if indexOID == "" {
			concrete := s.Git.ConcreteManager()
			if concrete == nil {
				return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeSourceVersionUnavailable, "Git index inspection is unavailable."}
			}
			var err error
			indexOID, err = concrete.IndexBlobOID(ctx, rootAbs, relRepoPath)
			if err != nil {
				return sourceledger.Comparison{}, err
			}
		}
		before = gitBlobComparisonSide(ctx, mgr, rootAbs, indexOID)
		before.RootID, before.Path = source.RootID, beforePath
		after = verifyHistoricalWorkingSide(readWorkingCommit(targetProject, source.RootID, rootAbs, source.Path).Side, source.ExpectedAfterSHA256)
	}

	return finalizeCommitComparison(before, after), nil
}

func unavailableWorkingSide(rootID, path string, expectedAfterSHA256 *string) sourceledger.ComparisonSide {
	if expectedAfterSHA256 != nil && *expectedAfterSHA256 == "" {
		return sourceledger.ComparisonSide{
			RootID:       rootID,
			Path:         path,
			State:        "absent",
			Availability: sourceledger.ContentAbsent,
		}
	}
	return sourceledger.ComparisonSide{
		RootID:       rootID,
		Path:         path,
		State:        "content",
		Availability: sourceledger.ContentUnavailable,
		Reason:       "content_unavailable",
	}
}

func verifyHistoricalWorkingSide(side sourceledger.ComparisonSide, expectedAfterSHA256 *string) sourceledger.ComparisonSide {
	if expectedAfterSHA256 == nil {
		return side
	}
	want := strings.TrimSpace(*expectedAfterSHA256)
	if want == "" {
		if side.State == "absent" {
			return side
		}
		return unavailableWorkingSide(side.RootID, side.Path, expectedAfterSHA256)
	}
	if side.State != "content" || side.SHA256 != want {
		return unavailableWorkingSide(side.RootID, side.Path, expectedAfterSHA256)
	}
	return side
}

func finalizeCommitComparison(before, after sourceledger.ComparisonSide) sourceledger.Comparison {
	op := wire.SourceChangeOpWrite
	if after.State == "absent" {
		op = wire.SourceChangeOpDelete
	} else if before.State == "absent" {
		op = wire.SourceChangeOpCreate
	}
	return sourceledger.Comparison{InRange: true, Op: op, Before: before, After: after}
}

func (s *Comparisons) loadBlobComparison(ctx context.Context, p *project.Project, source wire.BlobComparisonSource) (sourceledger.Comparison, error) {
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
	if source.BeforeBlobOid == "" && source.BlobOid != "" {
		diff.Op = wire.SourceChangeOpCreate
	} else if source.BlobOid == "" && source.BeforeBlobOid != "" {
		diff.Op = wire.SourceChangeOpDelete
	}
	return diff, nil
}

func (s *Comparisons) writeSourceReviewedComparison(w http.ResponseWriter, r *http.Request, p *project.Project, fileID string) {
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
