package sourceapi

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/sourcetree"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func validateTreeReview(scope *wire.SourceTreeReviewScope) error {
	if scope == nil {
		return nil
	}
	baseline, err := sourceledger.ParseBaseline(scope.Baseline)
	if err != nil {
		return rejectedSourceIntent(err.Error())
	}
	if baseline.Kind == sourceledger.BaselineCommit && scope.MarkUserEdits != nil && !*scope.MarkUserEdits {
		return rejectedSourceIntent("A commit baseline includes all edits because Git records no edit author.")
	}
	return nil
}
func treeReviewPreparing() error {
	return &comparisonFailure{wire.ApiErrorCodeSourceViewPreparing, "The source review is being prepared."}
}
func (view *sourceView) changeTreeReview(scope *wire.SourceTreeReviewScope) *sourcetree.Filtered {
	previous := view.changeTreeFilter(view.treeIntent.Filter)
	view.treeIntent.Review = scope
	view.reviewGeneration = uuid.NewString()
	view.reviewDirty, view.reviewPreparing = true, true
	view.state = "preparing"
	if view.reviewCancel != nil {
		view.reviewCancel()
	}
	return previous
}

func (s *Handler) refreshTreeReview(view *sourceView) {
	view.mu.Lock()
	if view.ctx.Err() != nil || !view.treePrepared || !view.reviewDirty || view.reviewRunning {
		view.mu.Unlock()
		return
	}
	view.reviewRunning = true
	view.mu.Unlock()
	_, release, err := s.sourceViewRegistry().registry.Acquire(view.scope, view.id)
	if err != nil {
		view.mu.Lock()
		view.reviewRunning = false
		view.mu.Unlock()
		return
	}
	s.background.Go(view.ctx, func(ctx context.Context) { defer release(); s.prepareTreeReview(ctx, view) })
}

func (s *Handler) prepareTreeReview(lifetime context.Context, view *sourceView) {
	for {
		view.mu.Lock()
		if view.ctx.Err() != nil || !view.reviewDirty {
			view.reviewRunning = false
			view.reviewCancel = nil
			view.mu.Unlock()
			return
		}
		scope, generation := view.treeIntent.Review, view.reviewGeneration
		ctx, cancel := context.WithCancel(lifetime)
		view.reviewCancel, view.reviewDirty = cancel, false
		view.mu.Unlock()
		review, err := s.loadTreeReview(ctx, view, scope)
		view.intentMu.Lock()
		view.mu.Lock()
		current := generation == view.reviewGeneration && view.ctx.Err() == nil
		view.mu.Unlock()
		if !current {
			view.intentMu.Unlock()
			cancel()
			if review != nil {
				review.Close()
			}
			continue
		}
		if err == nil {
			err = view.tree.SetReview(review)
		}
		view.intentMu.Unlock()
		if err != nil && review != nil {
			review.Close()
		}
		// Weighted additions are ready before the accepted review scope serves rows.
		if err == nil {
			for {
				_, _, err = view.tree.Revision(ctx)
				if !errors.Is(err, pagedview.ErrRevision) && !errors.Is(err, pagedview.ErrPreparing) {
					break
				}
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					err = ctx.Err()
				case <-timer.C:
				}
				if ctx.Err() != nil {
					break
				}
			}
		}
		cancel()
		view.mu.Lock()
		current = generation == view.reviewGeneration && view.ctx.Err() == nil
		if current {
			if err != nil {
				view.failLocked(err)
			} else {
				view.reviewPreparing = false
				view.failure = nil
				if view.treeIntent.Filter == "" {
					view.state = "ready"
				}
			}
		}
		view.mu.Unlock()
		if current {
			if err == nil {
				s.refreshTreeFilter(view)
			}
			view.notifier.Notify(true)
		}
	}
}

func (s *Handler) loadTreeReview(ctx context.Context, view *sourceView, scope *wire.SourceTreeReviewScope) (*sourcetree.ReviewSet, error) {
	if scope == nil {
		return nil, nil
	}
	p, err := s.ProjectRegistry.Get(ctx, view.scope.Project)
	if err == nil {
		p, err = s.sourceViewProject(ctx, p, view)
	}
	if err != nil {
		return nil, err
	}
	baseline, err := sourceledger.ParseBaseline(scope.Baseline)
	if err != nil {
		return nil, err
	}
	baseline.RootBranches = workspaceSourceBranches(p)
	baseline.WithoutUserEdits = scope.MarkUserEdits != nil && !*scope.MarkUserEdits
	builder := view.tree.ReviewBuilder(ctx)
	defer builder.Close()
	if baseline.Kind == sourceledger.BaselineCommit {
		if err := s.addCommitReviewPaths(ctx, p, builder); err != nil {
			return nil, err
		}
	} else {
		before := int64(0)
		for {
			page, err := s.SourceLedger.DeletedPaths(ctx, p.ID, baseline, 200, before)
			if err != nil {
				return nil, err
			}
			for _, path := range page.Paths {
				if err := addAbsentReviewPath(p, builder, path.RootID, path.Path); err != nil {
					return nil, err
				}
			}
			if page.NextBeforeOrdinal == 0 {
				break
			}
			before = page.NextBeforeOrdinal
		}
	}
	return builder.Finish()
}
func addAbsentReviewPath(p *project.Project, builder *sourcetree.ReviewBuilder, root, path string) error {
	_, err := project.ResolveAbsentSourcePath(p, root, path)
	if errors.Is(err, project.ErrSourceExists) || errors.Is(err, project.ErrSourcePathDenied) || errors.Is(err, project.ErrSourceNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return builder.Add(sourcetree.Address{Root: root, Path: path})
}
func (s *Handler) addCommitReviewPaths(ctx context.Context, p *project.Project, builder *sourcetree.ReviewBuilder) error {
	paths, roots := s.collectCommitPaths(ctx, p)
	for _, root := range roots {
		if !root.Available {
			return &comparisonFailure{wire.ApiErrorCodeSourceUnavailable, root.Error}
		}
	}
	for _, path := range paths {
		if !path.submodule && strings.ContainsAny(path.status, "DR") {
			if err := addAbsentReviewPath(p, builder, path.rootID, path.path); err != nil {
				return err
			}
		}
	}
	return nil
}
