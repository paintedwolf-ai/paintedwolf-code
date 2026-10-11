package sourceapi

import (
	"context"
	"reflect"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/secretview"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecomparison"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type comparisonScreenKey struct {
	scope                           pagedview.Scope
	evidence, before, after         string
	beforeAvailable, afterAvailable bool
}

type comparisonScreenResult struct{ before, after *wire.SecretScreen }

// Screening publishes annotations after the comparison becomes readable.
func (s *ComparisonViews) screenComparisonView(ctx context.Context, view *sourceView) {
	if s.SecretSpans == nil || !s.SecretSpans.Ready() {
		return
	}
	view.screening.secretScreenMu.Lock()
	defer view.screening.secretScreenMu.Unlock()
	s.screenComparisonLocked(ctx, view)
}

// A read schedules fresh annotations without waiting for initial preparation.
func (s *ComparisonViews) refreshComparisonScreen(view *sourceView) {
	if s.SecretSpans == nil || !s.SecretSpans.Ready() || !view.screening.secretScreenMu.TryLock() {
		return
	}
	if view.screening.secretScreenKey == nil {
		view.screening.secretScreenMu.Unlock()
		return
	}
	_, release, err := s.Views.sourceViewRegistry().registry.Acquire(view.scope, view.id)
	if err != nil {
		view.screening.secretScreenMu.Unlock()
		return
	}
	s.background.Go(view.ctx, func(ctx context.Context) {
		defer release()
		defer view.screening.secretScreenMu.Unlock()
		ctx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(view.ctx, cancel) //nolint:contextcheck // View release cancels its annotations.
		defer cancel()
		defer stop()
		s.screenComparisonLocked(ctx, view)
	})
}

func (s *ComparisonViews) screenComparisonLocked(ctx context.Context, view *sourceView) {
	view.mu.Lock()
	before, after := view.comparisonData.comparisonBefore, view.comparisonData.comparisonAfter
	readable := view.comparisonData.comparison != nil
	view.mu.Unlock()
	if !readable {
		return
	}
	screenCtx, evidence, err := secretview.ProjectContext(s.ManagedSecrets, ctx, view.scope.Project)
	if err != nil {
		return
	}
	key := comparisonScreenKey{scope: view.scope, evidence: evidence + s.SecretSpans.ClassificationRevision(screenCtx),
		before: sourcecomparison.Hash(before.Content), after: sourcecomparison.Hash(after.Content),
		beforeAvailable: before.Availability == "available", afterAvailable: after.Availability == "available"}
	if view.screening.secretScreenKey != nil && *view.screening.secretScreenKey == key {
		return
	}
	screens, err := s.sourceViews.comparisonScreens.Do(screenCtx, key, func(work context.Context) (comparisonScreenResult, error) {
		result := comparisonScreenResult{}
		if before.Availability == "available" {
			result.before = secretview.ScreenText(s.SecretSpans, work, before.Content)
		}
		if after.Availability == "available" {
			result.after = secretview.ScreenText(s.SecretSpans, work, after.Content)
		}
		return result, work.Err()
	})
	if err != nil {
		return
	}
	_, currentEvidence, currentErr := secretview.ProjectContext(s.ManagedSecrets, ctx, view.scope.Project)
	if currentErr != nil || currentEvidence+s.SecretSpans.ClassificationRevision(screenCtx) != key.evidence {
		return
	}
	if reflect.DeepEqual(before.SecretScreen, screens.before) && reflect.DeepEqual(after.SecretScreen, screens.after) {
		view.screening.secretScreenKey = &key
		return
	}
	if err := view.publishComparisonScreens(ctx, screens); err == nil {
		view.screening.secretScreenKey = &key
		view.notifier.Notify(true)
	}
}

func (view *sourceView) publishComparisonScreens(ctx context.Context, screens comparisonScreenResult) error {
	view.intentMu.Lock()
	defer view.intentMu.Unlock()
	view.mu.Lock()
	defer view.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	projection, err := view.comparisonProjection(ctx, view.comparisonData.comparison, view.comparisonData.comparisonIntent, screens.before, screens.after)
	if err != nil {
		return err
	}
	before, after := view.comparisonData.comparisonBefore, view.comparisonData.comparisonAfter
	before.SecretScreen, after.SecretScreen = screens.before, screens.after
	details := *view.comparisonData.details
	details.Before, details.After = readerEndpoint(before), readerEndpoint(after)
	previous := view.comparisonData.projection
	view.comparisonData.projection, view.comparisonData.details = projection, &details
	view.comparisonData.comparisonBefore, view.comparisonData.comparisonAfter = before, after
	view.projectionRevision = uuid.NewString()
	previous.release()
	return nil
}
