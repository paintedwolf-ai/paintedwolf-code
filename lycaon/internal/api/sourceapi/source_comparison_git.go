package sourceapi

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Comparisons) loadSourceGitReview(ctx context.Context, p *project.Project, id string, movement *bool, parent *int) (sourceGitReviewRequest, error) {
	var req sourceGitReviewRequest
	changes, err := s.SourceLedger.Git.GitTransitionsByIDs(ctx, []string{id})
	if err != nil {
		return req, err
	}
	change, exists := changes[id]
	if !exists || change.ProjectID != p.ID {
		return req, &comparisonFailure{wire.ApiErrorCodeGitMovementNotFound, "Git movement not found."}
	}
	req.change = change
	if change.ToCommit == "" {
		return req, &comparisonFailure{wire.ApiErrorCodeSourceVersionUnavailable, "No destination commit was recorded for this Git movement."}
	}
	mgr := s.Git.Manager()
	rootAbs, _, mapped := s.resolveRootRepoPosition(ctx, p, change.RootID)
	if !mapped {
		return req, &comparisonFailure{wire.ApiErrorCodeSourceVersionUnavailable, "The repository for this Git movement is unavailable."}
	}
	req.mgr, req.rootID, req.rootAbs = mgr, change.RootID, rootAbs
	commit, err := mgr.CommitDetails(ctx, rootAbs, change.ToCommit)
	if err != nil {
		return req, &comparisonFailure{wire.ApiErrorCodeSourceVersionUnavailable, "Commit details could not be read from this repository."}
	}
	req.commit = commit
	return selectSourceGitReview(req, movement, parent)
}

func selectSourceGitReview(req sourceGitReviewRequest, movement *bool, selectedParent *int) (sourceGitReviewRequest, error) {
	req.commitComparison = commitReviewDefault(req.change.Kind) || req.change.FromCommit == ""
	if movement != nil {
		req.commitComparison = !*movement
	}
	parent := 1
	if selectedParent != nil {
		parent = *selectedParent
		if !req.commitComparison || parent < 1 || parent > len(req.commit.Parents) {
			return req, &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "Select a parent of this commit."}
		}
	}
	req.opts = git.CommitReviewOptions{Before: req.change.FromCommit, After: req.change.ToCommit, Limit: 100}
	if req.commitComparison {
		req.opts.Before = ""
		if len(req.commit.Parents) > 0 {
			req.opts.Before = req.commit.Parents[parent-1]
		}
	} else if req.opts.Before == "" {
		return req, &comparisonFailure{wire.ApiErrorCodeSourceVersionUnavailable, "The previous commit for this movement was not recorded."}
	}
	return req, nil
}

func (s *Comparisons) loadGitChangeComparison(ctx context.Context, p *project.Project, source wire.GitChangeComparisonSource) (sourceledger.Comparison, error) {
	if err := gitReviewPathFailure(source.Path); err != nil {
		return sourceledger.Comparison{}, err
	}
	req, err := s.loadSourceGitReview(ctx, p, source.ChangeID, source.Movement, source.Parent)
	if err != nil {
		return sourceledger.Comparison{}, err
	}
	return loadGitReviewFile(ctx, req, source.Path)
}

func gitReviewPathFailure(path string) error {
	if !filepath.IsLocal(path) || path == "." || strings.ContainsRune(path, '\x00') {
		return &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "A root-relative file path is required."}
	}
	return nil
}

func loadGitReviewFile(ctx context.Context, req sourceGitReviewRequest, path string) (sourceledger.Comparison, error) {
	req.opts.Path = path
	page, err := req.mgr.CommitReview(ctx, req.rootAbs, req.opts)
	if err != nil {
		return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeSourceVersionUnavailable, "The Git comparison could not be read."}
	}
	if len(page.Files) != 1 {
		return sourceledger.Comparison{}, &comparisonFailure{wire.ApiErrorCodeSourceNotFound, "This file is not part of the Git comparison."}
	}
	file := page.Files[0]
	before := gitReviewComparisonSide(ctx, req, file.BeforeOID, file.BeforeMode, file.BeforePath)
	after := gitReviewComparisonSide(ctx, req, file.AfterOID, file.AfterMode, file.Path)
	return sourceledger.Comparison{InRange: true, Op: mapGitReviewFile(file).Op, LocationChanged: file.BeforePath != file.Path, Before: before, After: after}, nil
}
