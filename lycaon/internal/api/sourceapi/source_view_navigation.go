package sourceapi

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcetree"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// A displayed basis supersedes later disclosure intent once. Consecutive
// commands from that same presentation compose on the accepted result.
func (s *Handler) applySourceTreeUpdate(r *http.Request, view *sourceView, request wire.SourceTreeViewUpdate) (*sourcetree.Filtered, error) {
	var basis *sourcetree.RulesBasis
	if request.BasePresentationID != "" {
		if _, err := uuid.Parse(request.BasePresentationID); err != nil {
			return nil, rejectedSourceIntent("base_presentation_id must be a UUID.")
		}
		if request.Command.Filter != nil || request.Command.Review != nil {
			return nil, rejectedSourceIntent("A presentation basis requires a navigation command.")
		}
		presentation, release, err := s.acquireBasisPresentation(view, request.BasePresentationID)
		if err != nil {
			return nil, err
		}
		defer release()
		if presentation.summary.Tree == nil {
			return nil, pagedview.ErrRevision
		}
		if request.Command.Toggle != nil || ManualSourceTreeDisclosure(request.Command.Disclose) {
			basis = &sourcetree.RulesBasis{Replace: view.treeNavigationBasis != request.BasePresentationID}
			for _, entry := range treeDisclosures(presentation.summary.Tree.Intent.Disclosures) {
				basis.Rules.Set(entry.Address, entry.Disclosure)
			}
		}
	}
	previous, err := s.applySourceTreeCommand(r, view, basis, request.Command)
	if err != nil {
		return previous, err
	}
	if basis != nil {
		view.treeNavigationBasis = request.BasePresentationID
	} else {
		view.treeNavigationBasis = ""
	}
	return previous, nil
}

func ManualSourceTreeDisclosure(command *wire.SourceTreeDisclose) bool {
	if command == nil || len(command.Disclosures) == 0 {
		return false
	}
	for _, disclosure := range command.Disclosures {
		if disclosure.Recursive {
			return false
		}
	}
	return true
}
