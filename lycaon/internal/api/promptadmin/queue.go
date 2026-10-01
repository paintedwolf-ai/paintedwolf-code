package promptadmin

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	queuestore "github.com/lycaon/lycaon/internal/queue"
	"github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleGetQueue(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !requestscope.SessionExists(s.Store, s.responses, w, r, id) {
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.Sessions.QueueSnapshot(id))
}

func (s *Handler) HandleUpdateSessionQueue(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req api.QueueMutateRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if !requestscope.SessionExists(s.Store, s.responses, w, r, id) {
		return
	}

	ctx := r.Context()
	var (
		draft api.QueueDraft
		err   error
	)
	switch req.Op {
	case "reorder":
		draft, err = s.Sessions.QueueReorder(ctx, id, req.ExpectedRevision, req.ItemIDs)
	case "link":
		draft, err = s.Sessions.QueueLink(ctx, id, req.ExpectedRevision, req.ItemIDs)
	case "unlink":
		draft, err = s.Sessions.QueueUnlink(ctx, id, req.ExpectedRevision, req.ItemIDs)
	case "remove":
		if len(req.ItemIDs) == 0 {
			s.responses.Fail(w, api.ApiErrorCodeInvalidRequest, "remove requires item_ids")
			return
		}
		draft, err = s.Sessions.QueueRemove(ctx, id, req.ExpectedRevision, req.ItemIDs)
	case "update":
		if len(req.ItemIDs) != 1 || req.Text == nil {
			s.responses.Fail(w, api.ApiErrorCodeInvalidRequest, "update requires one item id and text")
			return
		}
		draft, err = s.Sessions.QueueUpdateText(ctx, id, req.ExpectedRevision, req.ItemIDs[0], *req.Text)
	case "hold":
		if req.Hold == nil {
			s.responses.Fail(w, api.ApiErrorCodeInvalidRequest, "hold requires hold")
			return
		}
		draft, err = s.Sessions.QueueSetHold(ctx, id, req.ExpectedRevision, *req.Hold)
	case "fire_now":
		draft, err = s.Sessions.QueueFireNow(ctx, id, req.ExpectedRevision, req.ItemIDs)
	case "send":
		draft, err = s.Sessions.QueueSend(ctx, id, req.ExpectedRevision)
	case "cancel_send":
		draft, err = s.Sessions.QueueCancelSend(ctx, id, req.ExpectedRevision)
	default:
		s.responses.FailReason(w, api.ApiErrorCodeInvalidRequest, "unknown op: "+req.Op)
		return
	}
	if err != nil {
		s.writeQueueError(w, r, err, draft)
		return
	}

	s.MaybeDrainQueue(ctx, id)
	httpio.WriteJSON(w, http.StatusOK, draft)
}

// writeQueueError maps queue rejections to client-rendered error codes.
func (s *Handler) writeQueueError(w http.ResponseWriter, r *http.Request, err error, draft api.QueueDraft) {
	switch {
	case errors.Is(err, queuestore.ErrRevisionConflict):
		var conflict *queuestore.RevisionConflictError
		_ = errors.As(err, &conflict)
		details := map[string]any{"current_revision": draft.Revision}
		if conflict != nil {
			details["expected_revision"] = conflict.Expected
		}
		s.responses.FailDetails(w, api.ApiErrorCodeQueueRevisionConflict, details, "the queue changed since it was read")
	case errors.Is(err, queuestore.ErrNoDraft), errors.Is(err, queuestore.ErrNothingToSend):
		s.responses.Fail(w, api.ApiErrorCodeQueueEmpty, "the queue has nothing to send")
	case errors.Is(err, queuestore.ErrSendPending):
		s.responses.Fail(w, api.ApiErrorCodeQueueSendPending, "a queued send is already pending")
	case errors.Is(err, queuestore.ErrSendReserved):
		s.responses.Fail(w, api.ApiErrorCodeQueueSendReserved, "the queued send is already being delivered")
	case errors.Is(err, queuestore.ErrNoSendToCancel):
		s.responses.Fail(w, api.ApiErrorCodeQueueNoSendToCancel, "there is no queued send to cancel")
	default:
		s.responses.InternalError(w, r, err)
	}
}

// MaybeDrainQueue drains only when no prompt loop can consume Send.
func (s *Handler) MaybeDrainQueue(parent context.Context, id string) {
	if s.Sessions.PromptState().Running(id) {
		return
	}
	s.background.Go(parent, func(ctx context.Context) {
		s.Sessions.DrainQueue(ctx, id)
	})
}
