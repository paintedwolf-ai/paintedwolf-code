package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/people/personactions"
	"github.com/lycaon/lycaon/internal/watch"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (h *Watches) handleListWatches(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireSessionByURLID(w, r)
	if !ok {
		return
	}
	if h.watches == nil || !h.watches.Configured() {
		httpio.WriteJSON(w, http.StatusOK, wire.AgentWatchListResponse{Watches: []wire.AgentWatch{}})
		return
	}
	ctx := r.Context()
	regs, err := watch.ListSessionAgentWatches(ctx, h.watches, sess.ID)
	if err != nil {
		h.responses.InternalError(w, r, err)
		return
	}
	items := make([]wire.AgentWatch, len(regs))
	for i, reg := range regs {
		items[i] = watch.AgentWatchToWire(ctx, h.watches, reg)
	}
	httpio.WriteJSON(w, http.StatusOK, wire.AgentWatchListResponse{Watches: items})
}

func (h *Watches) handleGetWatch(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireSessionByURLID(w, r)
	if !ok {
		return
	}
	handle, ok := h.requireWatchHandle(w, r)
	if !ok {
		return
	}
	if h.watches == nil || !h.watches.Configured() {
		h.responses.Fail(w, wire.ApiErrorCodeWatchNotFound, "watch not found")
		return
	}
	ctx := r.Context()
	reg, err := watch.GetSessionAgentWatch(ctx, h.watches, sess.ID, handle)
	if err != nil {
		h.failWatchLookupError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, watch.AgentWatchToWire(ctx, h.watches, reg))
}

func (h *Watches) handleUpdateWatchDelivery(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireSessionSendAuthorized(w, r)
	if !ok {
		return
	}
	handle, ok := h.requireWatchHandle(w, r)
	if !ok {
		return
	}
	var req wire.UpdateWatchDeliveryRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		h.responses.DecodeError(w, r, err)
		return
	}
	mode := watch.DeliveryMode(strings.TrimSpace(req.Delivery))
	if mode != watch.DeliveryCollect && mode != watch.DeliveryWake {
		h.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "delivery must be collect or wake")
		return
	}
	if h.watches == nil || !h.watches.Configured() {
		h.responses.Fail(w, wire.ApiErrorCodeWatchNotFound, "watch not found")
		return
	}
	ctx := r.Context()
	reg, err := watch.SetSessionAgentWatchDelivery(ctx, h.watches, sess.ID, handle, mode)
	if err != nil {
		h.failMutationWatchError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, watch.AgentWatchToWire(ctx, h.watches, reg))
}

func (h *Watches) handleCancelWatch(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireSessionSendAuthorized(w, r)
	if !ok {
		return
	}
	handle, ok := h.requireWatchHandle(w, r)
	if !ok {
		return
	}
	if h.watches == nil || h.watches.Standing() == nil {
		h.responses.Fail(w, wire.ApiErrorCodeWatchNotFound, "watch not found")
		return
	}
	ctx := r.Context()
	rec, err := h.watches.Standing().CancelByPerson(ctx, sess.ID, handle)
	if err != nil {
		h.failActionWatchError(w, r, err)
		return
	}
	reg, err := watch.GetSessionAgentWatch(ctx, h.watches, sess.ID, handle)
	if err != nil {
		h.failActionWatchError(w, r, err)
		return
	}
	aw := watch.AgentWatchToWire(ctx, h.watches, reg)
	var wireRec wire.AgentWatchReceipt
	if aw.FinalReceipt != nil {
		wireRec = *aw.FinalReceipt
	} else {
		wireRec = wire.AgentWatchReceipt{
			Handle:          rec.Handle,
			State:           string(rec.State),
			TerminalOutcome: string(rec.Outcome),
			StandingWatchID: rec.StandingWatchID,
		}
	}
	httpio.WriteJSON(w, http.StatusOK, wire.CancelWatchResponse{
		Watch:   aw,
		Receipt: wireRec,
	})
}

func (h *Watches) handleKeepWatch(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireSessionSendAuthorized(w, r)
	if !ok {
		return
	}
	handle, ok := h.requireWatchHandle(w, r)
	if !ok {
		return
	}
	var req wire.KeepWatchRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		h.responses.DecodeError(w, r, err)
		return
	}
	if h.watches == nil || h.watches.Standing() == nil {
		h.responses.Fail(w, wire.ApiErrorCodeWatchNotFound, "watch not found")
		return
	}
	ctx := r.Context()
	caller := requestscope.Caller(r)
	in, baseRev := watch.KeepRequestToInput(sess.ProjectID, req)
	sw, rec, err := h.watches.Standing().KeepAgentWatch(ctx, caller.ID, sess.ID, handle, in, baseRev)
	if err != nil {
		h.failMutationWatchError(w, r, err)
		return
	}
	personactions.Note(ctx, "watch_id", sw.WatchID)
	reg, _ := watch.GetSessionAgentWatch(ctx, h.watches, sess.ID, handle)
	_, latestMatch, recAt, _ := watch.LoadFinalReceipt(ctx, h.watches.Database(), reg.WatchID)
	wireRec := watch.ReceiptToWire(reg, &rec, latestMatch, recAt)
	if wireRec == nil {
		wireRec = &wire.AgentWatchReceipt{
			Handle:          rec.Handle,
			State:           string(rec.State),
			TerminalOutcome: string(rec.Outcome),
			StandingWatchID: sw.WatchID,
		}
	}
	runsByWatch, _ := watch.ListWatchRunsWindow(ctx, h.watches, sess.ProjectID, h.watches.Clock().Now())
	httpio.WriteJSON(w, http.StatusOK, wire.KeepWatchResponse{
		StandingWatch: watch.StandingWatchToWire(sw, runsByWatch[sw.WatchID]),
		Receipt:       *wireRec,
	})
}

func (h *Watches) requireSessionByURLID(w http.ResponseWriter, r *http.Request) (*wire.Session, bool) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		h.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "session id is required")
		return nil, false
	}
	return requestscope.Session(h.sessionStore, h.responses, w, r, id)
}

func (h *Watches) requireSessionSendAuthorized(w http.ResponseWriter, r *http.Request) (*wire.Session, bool) {
	caller := requestscope.Caller(r)
	if !people.MayInvoke(caller, operationSendPrompt.ID) {
		h.responses.Fail(w, wire.ApiErrorCodeForbidden, "caller cannot send in this chat")
		return nil, false
	}
	return h.requireSessionByURLID(w, r)
}

func (h *Watches) requireWatchHandle(w http.ResponseWriter, r *http.Request) (string, bool) {
	handle := strings.TrimSpace(chi.URLParam(r, "watch_handle"))
	if handle == "" {
		h.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "watch_handle is required")
		return "", false
	}
	return handle, true
}

func (h *Watches) failWatchLookupError(w http.ResponseWriter, r *http.Request, err error) {
	var notFound *watch.UnknownHandleError
	if errors.As(err, &notFound) {
		h.responses.Fail(w, wire.ApiErrorCodeWatchNotFound, notFound.Error())
		return
	}
	h.responses.InternalError(w, r, err)
}
