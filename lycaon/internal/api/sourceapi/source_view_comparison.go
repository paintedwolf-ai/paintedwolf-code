package sourceapi

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcecomparison"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *ComparisonViews) newComparisonView(scope pagedview.Scope, p *project.Project, request wire.SourceComparisonViewCreate) *sourceView {
	ctx, cancel := context.WithCancel(s.background.Context())
	view := &sourceView{scope: scope, clientID: request.ClientID, sessionID: request.SessionID, workspaceID: p.WorkspaceID(),
		ctx: ctx, cancel: cancel, state: "preparing", commands: pagedview.NewCommands[string](&s.sourceViews.receipts), projectionRevision: uuid.NewString(), expires: time.Now().Add(sourceViewLifetime), comparisonData: sourceViewComparisonData{
			comparisonBudget: s.sourceReaders.Budget(), comparisonIntent: request.Intent, comparisonSource: request.Source}}
	view.notifier = pagedview.NewNotifier(250*time.Millisecond, func() { s.Views.publishSourceView(view) })
	return view
}

func (s *ComparisonViews) prepareComparisonView(view *sourceView, p *project.Project, release func()) {
	s.background.Go(view.ctx, func(ctx context.Context) {
		defer release()
		ctx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(view.ctx, cancel) //nolint:contextcheck // closing the view also cancels its preparation
		defer cancel()
		defer stop()
		err := s.prepareComparisonContent(ctx, view, p)
		if err != nil {
			view.fail(err)
		}
		view.notifier.Notify(true)
		if err == nil {
			s.screenComparisonView(ctx, view)
		}
	})
}

func (s *ComparisonViews) prepareComparisonContent(ctx context.Context, view *sourceView, p *project.Project) error {
	if view.comparisonData.comparisonSource.Current != nil {
		return s.prepareCurrentSource(view, p)
	}
	if source := view.comparisonData.comparisonSource.Retained; source != nil {
		held, release, err := s.acquireRetainedSource(view, source)
		if err != nil {
			return err
		}
		defer release()
		scoped, err := s.Views.sourceViewProject(ctx, p, held)
		if err != nil {
			return err
		}
		held.mu.Lock()
		current := held.comparisonData.current
		if current != nil && source.Comparison == "before" {
			current.retain()
		}
		held.mu.Unlock()
		if current != nil {
			if err := validateSourceTreeAddress(scoped, wire.SourceTreeAddress{RootID: current.stream.RootID, Path: current.stream.Path}); err != nil {
				if source.Comparison == "before" {
					current.close()
				}
				return err
			}
			if !current.stream.Current() {
				if source.Comparison == "before" {
					current.close()
				}
				return currentSourceChanged()
			}
			if source.Comparison != "before" {
				return rejectedSourceIntent("A current file snapshot has no historical comparison.")
			}
			s.installCurrentSource(view, current)
			return nil
		}
	}
	var comparison wire.SourceComparison
	var err error
	if view.comparisonData.comparisonSource.Retained != nil {
		comparison, err = s.loadRetainedViewComparison(ctx, p, view)
	} else {
		comparison, err = s.Comparisons.loadComparisonSource(ctx, p, view.sessionID, view.comparisonData.comparisonSource, nil)
	}
	if err != nil {
		return err
	}
	return s.installComparison(view, comparison)
}

func (s *ComparisonViews) acquireRetainedSource(view *sourceView, source *wire.RetainedComparisonSource) (*sourceView, func(), error) {
	held, release, err := s.Views.sourceViewRegistry().registry.Acquire(view.scope, source.ViewID)
	if err != nil {
		return nil, nil, err
	}
	if held.workspaceID != view.workspaceID {
		release()
		return nil, nil, rejectedSourceIntent("A comparison can only derive from a view in the same workspace.")
	}
	return held, release, nil
}

func (view *sourceView) fail(err error) {
	view.mu.Lock()
	defer view.mu.Unlock()
	view.failLocked(err)
}

func (view *sourceView) failLocked(err error) {
	view.state = "failed"
	view.failure = sourcePreparationFailure(err, "The source presentation could not be prepared.")
}

func (s *ComparisonViews) installComparison(view *sourceView, comparison wire.SourceComparison) error {
	details := &wire.SourceComparisonDetails{InRange: comparison.InRange, EffectID: comparison.EffectID, FileID: comparison.FileID,
		Op: comparison.Op, LocationChanged: comparison.LocationChanged, UserEditsUnmarked: comparison.UserEditsUnmarked, PresentationAfterOrdinal: comparison.PresentationAfterOrdinal}
	var document *sourcecomparison.Document
	var projection *sourceViewProjection
	var release func()
	var before, after wire.SourceComparisonSide
	if comparison.InRange && comparison.Before != nil && comparison.After != nil {
		var err error
		document, release, err = s.sourceReaders.Prepare(view.ctx, view.scope.Person, view.scope.Project, *comparison.Before, *comparison.After, comparison.Attribution)
		if err != nil {
			return err
		}
		projection, err = view.comparisonProjection(view.ctx, document, view.comparisonData.comparisonIntent, comparison.Before.SecretScreen, comparison.After.SecretScreen)
		if err != nil {
			release()
			return err
		}
		before, after = *comparison.Before, *comparison.After
		// Metadata belongs to the selected endpoints; immutable text is shared.
		before.Content, after.Content = document.Before.Content, document.After.Content
		summary := document.Summary
		details.Summary = &wire.SourceComparisonSummary{Before: summary.Before, After: summary.After, Added: summary.Added, Removed: summary.Removed,
			Rows: summary.Rows, ChangeAreas: summary.ChangeAreas, ChangeAreaCount: summary.ChangeAreaCount}
		details.Before, details.After = readerEndpoint(*comparison.Before), readerEndpoint(*comparison.After)
	}
	view.mu.Lock()
	view.comparisonData.comparison, view.comparisonData.comparisonRelease, view.comparisonData.projection, view.comparisonData.details = document, release, projection, details
	view.comparisonData.comparisonBefore, view.comparisonData.comparisonAfter = before, after
	if view.comparisonData.comparisonSource.Text != nil {
		reference := *view.comparisonData.comparisonSource.Text
		bytes := int64(0)
		for _, content := range []*string{reference.Before, reference.After} {
			if content != nil {
				bytes += 2 * int64(len(*content))
			}
		}
		if view.trimDescriptor != nil {
			_ = view.trimDescriptor(bytes)
		}
		reference.Before, reference.After = nil, nil
		view.comparisonData.comparisonSource.Text = &reference
	}
	view.state, view.projectionRevision = "ready", uuid.NewString()
	view.mu.Unlock()
	return nil
}

func (s *ComparisonViews) loadRetainedViewComparison(ctx context.Context, p *project.Project, view *sourceView) (wire.SourceComparison, error) {
	source := view.comparisonData.comparisonSource.Retained
	held, release, err := s.acquireRetainedSource(view, source)
	if err != nil {
		return wire.SourceComparison{}, err
	}
	defer release()
	if _, err := s.Views.sourceViewProject(ctx, p, held); err != nil {
		return wire.SourceComparison{}, err
	}
	held.mu.Lock()
	document := held.comparisonData.comparison
	if held.state != "ready" || document == nil {
		held.mu.Unlock()
		return wire.SourceComparison{}, &comparisonFailure{wire.ApiErrorCodeSourceViewPreparing, "The source comparison is not ready."}
	}
	before, after := held.comparisonData.comparisonBefore, held.comparisonData.comparisonAfter
	attribution := document.Attribution
	chat := held.comparisonData.chatSource
	if held.comparisonData.comparisonSource.Chat != nil {
		chat = held.comparisonData.comparisonSource.Chat
	}
	held.mu.Unlock()
	if source.Comparison == "current" {
		before, err = s.Comparisons.readerCurrentSide(ctx, p, source.RootID, source.Path)
		if err != nil {
			return wire.SourceComparison{}, err
		}
		attribution = nil
	} else if source.Comparison != "before" {
		return wire.SourceComparison{}, &comparisonFailure{wire.ApiErrorCodeInvalidRequest, "Unknown comparison mode."}
	}
	view.mu.Lock()
	view.comparisonData.chatSource = chat
	view.mu.Unlock()
	return wire.SourceComparison{InRange: true, Before: &before, After: &after, Attribution: attribution}, nil
}
