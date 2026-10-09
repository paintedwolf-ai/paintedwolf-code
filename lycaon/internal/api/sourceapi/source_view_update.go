package sourceapi

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcetree"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Trees) HandleApplySourceViewIntent(w http.ResponseWriter, r *http.Request) {
	var request wire.SourceViewUpdate
	if err := httpio.DecodeJSON(w, r, &request); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if err := request.Validate(); err != nil {
		s.Views.writeSourceViewError(w, r, rejectedSourceIntent(err.Error()))
		return
	}
	var id, expected string
	if request.Tree != nil {
		id, expected = request.Tree.OperationID, request.Tree.ExpectedIntentRevision
	} else {
		id, expected = request.Comparison.OperationID, request.Comparison.ExpectedIntentRevision
	}
	if err := validateSourceCommandIdentity(id, expected); err != nil {
		s.Views.writeSourceViewError(w, r, err)
		return
	}
	view, r, release, ok := s.Views.requestedSourceView(w, r)
	if !ok {
		return
	}
	defer release()
	if (request.Tree != nil) != (view.navigation.tree != nil) {
		s.Views.writeSourceViewError(w, r, rejectedSourceIntent("The command kind must match the source view."))
		return
	}
	canonical, err := sourceViewCanonical(request)
	if err != nil {
		s.Views.writeSourceViewError(w, r, err)
		return
	}
	var previous *sourcetree.Filtered
	changed := false
	view.intentMu.Lock()
	_, _, err = view.commands.Apply(r.Context(), id, expected, canonical, func(_ string) (string, error) {
		if view.ctx.Err() != nil {
			return "", pagedview.ErrExpired
		}
		if view.navigation.tree != nil && !view.navigation.treeInitialized {
			return "", &comparisonFailure{wire.ApiErrorCodeSourceViewPreparing, "The source tree is being prepared."}
		}
		var err error
		if request.Tree != nil {
			previous, err = s.applySourceTreeUpdate(r, view, *request.Tree)
		} else {
			err = view.applyComparisonIntent(r, request.Comparison.Intent)
		}
		changed = err == nil
		return view.id, err
	})
	view.intentMu.Unlock()
	if previous != nil {
		previous.Close()
	}
	if changed {
		if view.navigation.tree != nil {
			s.retainSourceWork(view, view.navigation.tree.Prepare())
			s.refreshTreeReview(view)
			s.refreshTreeFilter(view)
		}
		view.notifier.Notify(true)
	}
	if err != nil {
		s.Views.writeSourceViewError(w, r, err)
		return
	}
	state, err := view.snapshot(r.Context())
	if err != nil {
		s.Views.writeSourceViewError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, state)
}

func (view *sourceView) applyComparisonIntent(r *http.Request, intent wire.SourceComparisonIntent) error {
	if err := validateSourceComparisonIntent(intent); err != nil {
		return err
	}
	view.mu.Lock()
	defer view.mu.Unlock()
	if view.state != "ready" {
		return &comparisonFailure{wire.ApiErrorCodeSourceViewPreparing, "The source comparison is not ready."}
	}
	if view.comparisonData.comparison != nil {
		projection, err := view.comparisonProjection(r.Context(), view.comparisonData.comparison, intent, view.comparisonData.comparisonBefore.SecretScreen, view.comparisonData.comparisonAfter.SecretScreen)
		if err != nil {
			return err
		}
		previous := view.comparisonData.projection
		view.comparisonData.projection = projection
		previous.release()
	}
	view.comparisonData.comparisonIntent = intent
	view.projectionRevision = uuid.NewString()
	return nil
}

func (s *Trees) applySourceTreeCommand(r *http.Request, view *sourceView, basis *sourcetree.RulesBasis, command wire.SourceTreeCommand) (*sourcetree.Filtered, error) {
	if err := command.Validate(); err != nil {
		return nil, rejectedSourceIntent(err.Error())
	}
	switch {
	case command.Toggle != nil:
		return nil, view.navigation.tree.Toggle(r.Context(), basis, treeAddress(command.Toggle.Address), command.Toggle.CollapseDescendants)
	case command.Disclose != nil:
		if len(command.Disclose.Disclosures) == 0 || len(command.Disclose.Disclosures) > 2048 {
			return nil, pagedview.ErrRange
		}
		return nil, view.navigation.tree.Disclose(r.Context(), basis, treeDisclosures(command.Disclose.Disclosures)...)
	case command.Reveal != nil:
		return nil, view.navigation.tree.Reveal(r.Context(), treeAddress(command.Reveal.Address))
	case command.Filter != nil:
		if len(command.Filter.Query) > 4096 {
			return nil, pagedview.ErrRange
		}
		view.mu.Lock()
		previous := view.changeTreeFilter(command.Filter.Query)
		view.mu.Unlock()
		return previous, nil
	case command.Review != nil:
		if err := validateTreeReview(command.Review.Scope); err != nil {
			return nil, err
		}
		view.mu.Lock()
		previous := view.changeTreeReview(command.Review.Scope)
		view.mu.Unlock()
		return previous, nil
	default:
		return nil, rejectedSourceIntent("Unknown tree command.")
	}
}

func validateSourceCommandIdentity(id, expected string) error {
	if _, err := uuid.Parse(id); err != nil {
		return rejectedSourceIntent("operation_id must be a UUID.")
	}
	if _, err := uuid.Parse(expected); err != nil {
		return rejectedSourceIntent("expected_intent_revision must be a UUID.")
	}
	return nil
}
