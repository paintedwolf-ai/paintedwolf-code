package promptadmin

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleListDraftVersions(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	if !requestscope.SessionExists(s.Store, s.responses, w, r, sessionID) {
		return
	}
	slotID := chi.URLParam(r, "slot_id")
	// A draft slot is the coordinator message the versions superseded.
	if _, err := s.Store.GetMessage(r.Context(), sessionID, slotID); err != nil {
		if errors.Is(err, store.ErrMessageNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeMessageNotFound, "That message is not in this chat.")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	versions, err := s.Store.ListDraftVersions(r.Context(), sessionID, slotID)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if versions == nil {
		versions = []wire.DraftVersion{}
	}
	for i := range versions {
		versions[i].OutcomeLabel = guidance.DraftVersionOutcomeLabel(s.HintConfig, versions[i].OutcomeCode)
	}
	httpio.WriteJSON(w, http.StatusOK, wire.DraftVersionsResponse{Versions: versions})
}
