package sessionadmin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// HandleRewindSession restores one human-selected turn boundary.
func (s *Rewind) HandleRewindSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req wire.RewindSessionRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	switch {
	case uuid.Validate(req.OperationID) != nil:
		s.responses.InvalidField(w, "operation_id", "must be a UUID")
		return
	case uuid.Validate(req.MessageID) != nil:
		s.responses.InvalidField(w, "message_id", "must be a UUID")
		return
	case strings.TrimSpace(req.PlanDigest) == "":
		s.responses.InvalidField(w, "plan_digest", "is required")
		return
	}
	if req.Mode == "" {
		req.Mode = wire.RewindModeBeforeTurn
	}
	if req.Mode != wire.RewindModeBeforeTurn {
		s.responses.Fail(w, wire.ApiErrorCodeRewindModeUnsupported,
			"only before_turn rewind is supported")
		return
	}

	result, err := s.Sessions.RewindToPrompt(r.Context(), req.OperationID, id, req.MessageID, req.PlanDigest)
	if err != nil {
		s.writeRewindError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.RewindSessionResponse{
		RestoredPaths:         append([]string{}, result.RestoredPaths...),
		TruncatedMessageCount: result.TruncatedMessageCount,
		RestoredPrompt:        result.RestoredPrompt,
		RestoredContentParts:  result.RestoredContentParts,
		RestoredArtifactIDs:   result.RestoredArtifactIDs,
	})
}

func (s *Rewind) writeRewindError(w http.ResponseWriter, r *http.Request, err error) {
	var blocked *sourceledger.RewindBlockedError
	switch {
	case errors.As(err, &blocked):
		s.responses.Fail(w, wire.ApiErrorCodeRewindBlocked, "files changed since this turn block the rewind")
	case errors.Is(err, session.ErrRewindPlanChanged):
		s.responses.Fail(w, wire.ApiErrorCodeRewindPlanChanged, "the rewind plan changed; preview it again")
	case errors.Is(err, session.ErrSessionNotIdle):
		s.responses.Fail(w, wire.ApiErrorCodeSessionNotIdle, "chat has a turn in flight")
	case errors.Is(err, session.ErrRewindAnchorIneligible):
		s.responses.Fail(w, wire.ApiErrorCodeRewindAnchorIneligible, "this message cannot be a rewind point")
	case errors.Is(err, session.ErrRewindAnchorNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeRewindAnchorNotFound, "rewind point not found")
	case errors.Is(err, session.ErrRewindOperationConflict):
		s.responses.Fail(w, wire.ApiErrorCodeIdempotencyConflict, "operation_id was already used for a different rewind")
	case errors.Is(err, store.ErrSessionNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "chat not found")
	default:
		s.responses.InternalError(w, r, err)
	}
}

func (s *Rewind) HandlePreviewSessionRewind(w http.ResponseWriter, r *http.Request) {
	var req wire.RewindPreviewRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if uuid.Validate(req.MessageID) != nil {
		s.responses.InvalidField(w, "message_id", "must be a UUID")
		return
	}
	result, err := s.Sessions.PreviewRewind(r.Context(), chi.URLParam(r, "id"), req.MessageID)
	if err != nil {
		s.writeRewindError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, result)
}
