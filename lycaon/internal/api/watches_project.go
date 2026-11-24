package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/people/personactions"
	"github.com/lycaon/lycaon/internal/watch"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (h *Watches) handleListProjectWatches(w http.ResponseWriter, r *http.Request) {
	proj, ok := requestscope.ProjectByURLID(h.projectRegistry, h.responses, w, r)
	if !ok {
		return
	}
	ownerFilter := strings.TrimSpace(r.URL.Query().Get("owner"))
	if ownerFilter == "" {
		ownerFilter = "all"
	}
	if ownerFilter != "all" && ownerFilter != "agent" && ownerFilter != "person" {
		h.responses.Fail(w, wire.ApiErrorCodeInvalidQuery, "owner must be agent, person, or all")
		return
	}
	if h.watches == nil || !h.watches.Configured() {
		httpio.WriteJSON(w, http.StatusOK, wire.ProjectWatchListResponse{Watches: []wire.ProjectWatch{}})
		return
	}

	ctx := r.Context()
	now := h.watches.Clock().Now()
	runsByWatch, err := watch.ListWatchRunsWindow(ctx, h.watches, proj.ID, now.Add(-24*time.Hour))
	if err != nil {
		h.responses.InternalError(w, r, err)
		return
	}

	allStanding, err := h.watches.Standing().List(ctx, proj.ID, false)
	if err != nil {
		h.responses.InternalError(w, r, err)
		return
	}
	var dailyCapNano int64
	var hasUnpriced bool
	var enabledCount int
	for _, sw := range allStanding {
		if sw.State == watch.StateEnabled {
			enabledCount++
			if sw.AllowUnpriced {
				hasUnpriced = true
			} else {
				dailyCapNano += int64(sw.DailyRunCap) * watch.USDToNanoUSD(sw.RunSpendLimitUSD)
			}
		}
	}

	items := make([]wire.ProjectWatch, 0)
	if ownerFilter == "all" || ownerFilter == "person" {
		for _, sw := range allStanding {
			swWire := watch.StandingWatchToWire(sw, runsByWatch[sw.WatchID])
			items = append(items, wire.ProjectWatch{
				WatchID:       sw.WatchID,
				OwnerKind:     string(watch.OwnerPerson),
				ProjectID:     sw.ProjectID,
				Revision:      swWire.Revision,
				StandingWatch: &swWire,
			})
		}
	}
	if ownerFilter == "all" || ownerFilter == "agent" {
		agentRegs, aErr := watch.ListProjectAgentWatches(ctx, h.watches, proj.ID)
		if aErr != nil {
			h.responses.InternalError(w, r, aErr)
			return
		}
		for _, reg := range agentRegs {
			aw := watch.AgentWatchToWire(ctx, h.watches, reg)
			items = append(items, wire.ProjectWatch{
				WatchID:    reg.WatchID,
				OwnerKind:  string(reg.Owner.Kind),
				ProjectID:  reg.Owner.ProjectID,
				SessionID:  reg.Owner.SessionID,
				Handle:     reg.Handle,
				Revision:   aw.Revision,
				AgentWatch: &aw,
			})
		}
	}

	httpio.WriteJSON(w, http.StatusOK, wire.ProjectWatchListResponse{
		Watches:                  items,
		DailySpendCapNanoUsd:     dailyCapNano,
		HasUnpricedStandingWatch: hasUnpriced,
		EnabledStandingTotal:     enabledCount,
	})
}

func (h *Watches) handleCreateStandingWatch(w http.ResponseWriter, r *http.Request) {
	proj, ok := requestscope.ProjectByURLID(h.projectRegistry, h.responses, w, r)
	if !ok {
		return
	}
	var req wire.CreateStandingWatchRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		h.responses.DecodeError(w, r, err)
		return
	}
	if h.watches == nil || h.watches.Standing() == nil {
		h.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "standing watches unavailable")
		return
	}
	caller := requestscope.Caller(r)
	sw, err := h.watches.Standing().Create(r.Context(), caller.ID, watch.CreateRequestToInput(proj.ID, req))
	if err != nil {
		h.failMutationWatchError(w, r, err)
		return
	}
	personactions.Note(r.Context(), "watch_id", sw.WatchID)
	httpio.WriteJSON(w, http.StatusCreated, watch.StandingWatchToWire(sw, nil))
}

func (h *Watches) handleGetProjectWatch(w http.ResponseWriter, r *http.Request) {
	proj, ok := requestscope.ProjectByURLID(h.projectRegistry, h.responses, w, r)
	if !ok {
		return
	}
	watchID, ok := h.requireWatchUUID(w, r)
	if !ok {
		return
	}
	if h.watches == nil || !h.watches.Configured() {
		h.responses.Fail(w, wire.ApiErrorCodeWatchNotFound, "watch not found")
		return
	}
	ctx := r.Context()
	reg, err := watch.GetProjectWatchRegistration(ctx, h.watches, proj.ID, watchID)
	if err != nil {
		h.failWatchLookupError(w, r, err)
		return
	}
	if reg.Owner.Kind == watch.OwnerPerson {
		sw, sErr := h.watches.Standing().Get(ctx, proj.ID, watchID)
		if sErr != nil {
			h.failWatchLookupError(w, r, sErr)
			return
		}
		runsByWatch, _ := watch.ListWatchRunsWindow(ctx, h.watches, proj.ID, h.watches.Clock().Now().Add(-24*time.Hour))
		swWire := watch.StandingWatchToWire(sw, runsByWatch[sw.WatchID])
		httpio.WriteJSON(w, http.StatusOK, wire.ProjectWatch{
			WatchID:       sw.WatchID,
			OwnerKind:     string(watch.OwnerPerson),
			ProjectID:     sw.ProjectID,
			Revision:      swWire.Revision,
			StandingWatch: &swWire,
		})
		return
	}
	aw := watch.AgentWatchToWire(ctx, h.watches, reg)
	httpio.WriteJSON(w, http.StatusOK, wire.ProjectWatch{
		WatchID:    reg.WatchID,
		OwnerKind:  string(reg.Owner.Kind),
		ProjectID:  reg.Owner.ProjectID,
		SessionID:  reg.Owner.SessionID,
		Handle:     reg.Handle,
		Revision:   aw.Revision,
		AgentWatch: &aw,
	})
}

func (h *Watches) handleUpdateStandingWatch(w http.ResponseWriter, r *http.Request) {
	proj, ok := requestscope.ProjectByURLID(h.projectRegistry, h.responses, w, r)
	if !ok {
		return
	}
	watchID, ok := h.requireWatchUUID(w, r)
	if !ok {
		return
	}
	var req wire.UpdateStandingWatchRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		h.responses.DecodeError(w, r, err)
		return
	}
	if req.Revision <= 0 {
		h.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "revision is required")
		return
	}
	if h.watches == nil || h.watches.Standing() == nil {
		h.responses.Fail(w, wire.ApiErrorCodeWatchNotFound, "watch not found")
		return
	}
	caller := requestscope.Caller(r)
	patch, baseRev := watch.UpdateRequestToPatch(req)
	sw, err := h.watches.Standing().Update(r.Context(), caller.ID, proj.ID, watchID, baseRev, patch)
	if err != nil {
		h.failMutationWatchError(w, r, err)
		return
	}
	runsByWatch, _ := watch.ListWatchRunsWindow(r.Context(), h.watches, proj.ID, h.watches.Clock().Now().Add(-24*time.Hour))
	httpio.WriteJSON(w, http.StatusOK, watch.StandingWatchToWire(sw, runsByWatch[sw.WatchID]))
}

func (h *Watches) handleDeleteStandingWatch(w http.ResponseWriter, r *http.Request) {
	proj, ok := requestscope.ProjectByURLID(h.projectRegistry, h.responses, w, r)
	if !ok {
		return
	}
	watchID, ok := h.requireWatchUUID(w, r)
	if !ok {
		return
	}
	if h.watches == nil || h.watches.Standing() == nil {
		h.responses.Fail(w, wire.ApiErrorCodeWatchNotFound, "watch not found")
		return
	}
	if err := h.watches.Standing().Delete(r.Context(), proj.ID, watchID); err != nil {
		h.failActionWatchError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Watches) handlePauseStandingWatch(w http.ResponseWriter, r *http.Request) {
	proj, ok := requestscope.ProjectByURLID(h.projectRegistry, h.responses, w, r)
	if !ok {
		return
	}
	watchID, ok := h.requireWatchUUID(w, r)
	if !ok {
		return
	}
	var req wire.StandingWatchRevisionRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		h.responses.DecodeError(w, r, err)
		return
	}
	if req.Revision <= 0 {
		h.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "revision is required")
		return
	}
	if h.watches == nil || h.watches.Standing() == nil {
		h.responses.Fail(w, wire.ApiErrorCodeWatchNotFound, "watch not found")
		return
	}
	sw, err := h.watches.Standing().Pause(r.Context(), proj.ID, watchID, req.Revision)
	if err != nil {
		h.failActionWatchError(w, r, err)
		return
	}
	runsByWatch, _ := watch.ListWatchRunsWindow(r.Context(), h.watches, proj.ID, h.watches.Clock().Now().Add(-24*time.Hour))
	httpio.WriteJSON(w, http.StatusOK, watch.StandingWatchToWire(sw, runsByWatch[sw.WatchID]))
}

func (h *Watches) handleResumeStandingWatch(w http.ResponseWriter, r *http.Request) {
	proj, ok := requestscope.ProjectByURLID(h.projectRegistry, h.responses, w, r)
	if !ok {
		return
	}
	watchID, ok := h.requireWatchUUID(w, r)
	if !ok {
		return
	}
	var req wire.StandingWatchRevisionRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		h.responses.DecodeError(w, r, err)
		return
	}
	if req.Revision <= 0 {
		h.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "revision is required")
		return
	}
	if h.watches == nil || h.watches.Standing() == nil {
		h.responses.Fail(w, wire.ApiErrorCodeWatchNotFound, "watch not found")
		return
	}
	caller := requestscope.Caller(r)
	sw, err := h.watches.Standing().Resume(r.Context(), caller.ID, proj.ID, watchID, req.Revision)
	if err != nil {
		h.failMutationWatchError(w, r, err)
		return
	}
	runsByWatch, _ := watch.ListWatchRunsWindow(r.Context(), h.watches, proj.ID, h.watches.Clock().Now().Add(-24*time.Hour))
	httpio.WriteJSON(w, http.StatusOK, watch.StandingWatchToWire(sw, runsByWatch[sw.WatchID]))
}

func (h *Watches) handleRunStandingWatchNow(w http.ResponseWriter, r *http.Request) {
	proj, ok := requestscope.ProjectByURLID(h.projectRegistry, h.responses, w, r)
	if !ok {
		return
	}
	watchID, ok := h.requireWatchUUID(w, r)
	if !ok {
		return
	}
	if h.watches == nil || h.watches.Standing() == nil {
		h.responses.Fail(w, wire.ApiErrorCodeWatchNotFound, "watch not found")
		return
	}
	caller := requestscope.Caller(r)
	sw, err := h.watches.Standing().Get(r.Context(), proj.ID, watchID)
	if err != nil {
		h.failActionWatchError(w, r, err)
		return
	}
	run, err := h.watches.Standing().RunNow(r.Context(), caller.ID, proj.ID, watchID)
	if err != nil {
		h.failActionWatchError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, watch.WatchRunToWire(run, sw.Name))
}

func (h *Watches) handleListWatchRuns(w http.ResponseWriter, r *http.Request) {
	proj, ok := requestscope.ProjectByURLID(h.projectRegistry, h.responses, w, r)
	if !ok {
		return
	}
	watchID, ok := h.requireWatchUUID(w, r)
	if !ok {
		return
	}
	limit := 50
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > 50 {
			h.responses.Fail(w, wire.ApiErrorCodeInvalidQuery, "limit must be between 1 and 50")
			return
		}
		limit = parsed
	}
	if h.watches == nil || h.watches.Standing() == nil {
		h.responses.Fail(w, wire.ApiErrorCodeWatchNotFound, "watch not found")
		return
	}
	ctx := r.Context()
	sw, err := h.watches.Standing().Get(ctx, proj.ID, watchID)
	if err != nil {
		h.failActionWatchError(w, r, err)
		return
	}
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if cursor != "" && uuid.Validate(cursor) != nil {
		h.responses.Fail(w, wire.ApiErrorCodeInvalidPageCursor, "invalid page cursor")
		return
	}
	runs, nextCursor, err := h.watches.Standing().ListRuns(ctx, proj.ID, watchID, cursor, limit)
	if err != nil {
		var valErr *watch.ValidationError
		if errors.As(err, &valErr) && valErr.Field == "cursor" {
			h.responses.Fail(w, wire.ApiErrorCodeInvalidPageCursor, valErr.Error())
			return
		}
		h.failActionWatchError(w, r, err)
		return
	}
	wireRuns := make([]wire.WatchRun, len(runs))
	for i, run := range runs {
		wireRuns[i] = watch.WatchRunToWire(run, sw.Name)
	}
	httpio.WriteJSON(w, http.StatusOK, wire.WatchRunListResponse{
		Runs:       wireRuns,
		NextCursor: nextCursor,
	})
}

func (h *Watches) requireWatchUUID(w http.ResponseWriter, r *http.Request) (string, bool) {
	watchID := strings.TrimSpace(chi.URLParam(r, "watch_id"))
	if uuid.Validate(watchID) != nil {
		h.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "watch_id must be a UUID")
		return "", false
	}
	return watchID, true
}

func (h *Watches) failActionWatchError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		notFound    *watch.UnknownHandleError
		revConflict *watch.RevisionConflictError
		runActive   *watch.RunActiveError
		capReached  *watch.DailyCapReachedError
		nameTaken   *watch.NameTakenError
		notOwner    *watch.NotOwnerError
		notPerson   *watch.NotPersonOwnedError
		keepUnavail *watch.KeepUnavailableError
		termErr     *watch.TerminalError
		valErr      *watch.ValidationError
	)
	switch {
	case errors.As(err, &notFound):
		h.responses.Fail(w, wire.ApiErrorCodeWatchNotFound, notFound.Error())
	case errors.As(err, &revConflict):
		h.responses.Fail(w, wire.ApiErrorCodeWatchRevisionConflict, revConflict.Error())
	case errors.As(err, &runActive):
		h.responses.Fail(w, wire.ApiErrorCodeWatchRunActive, runActive.Error())
	case errors.As(err, &capReached):
		h.responses.Fail(w, wire.ApiErrorCodeWatchDailyCapReached, capReached.Error())
	case errors.As(err, &nameTaken):
		h.responses.Fail(w, wire.ApiErrorCodeWatchNameTaken, nameTaken.Error())
	case errors.As(err, &notOwner):
		h.responses.Fail(w, wire.ApiErrorCodeWatchNotOwner, notOwner.Error())
	case errors.As(err, &notPerson):
		h.responses.Fail(w, wire.ApiErrorCodeWatchNotPersonOwned, notPerson.Error())
	case errors.As(err, &keepUnavail):
		h.responses.Fail(w, wire.ApiErrorCodeWatchKeepUnavailable, keepUnavail.Error())
	case errors.As(err, &termErr):
		h.responses.Fail(w, wire.ApiErrorCodeWatchKeepUnavailable, termErr.Error())
	case errors.As(err, &valErr):
		h.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, valErr.Error())
	default:
		h.responses.InternalError(w, r, err)
	}
}

func (h *Watches) failMutationWatchError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		spendUnenf *watch.SpendUnenforceableError
		valErr     *watch.ValidationError
	)
	switch {
	case errors.As(err, &spendUnenf):
		h.responses.Fail(w, wire.ApiErrorCodeWatchSpendUnenforceable, spendUnenf.Error())
	case errors.As(err, &valErr):
		h.responses.Fail(w, wire.ApiErrorCodeWatchConditionInvalid, valErr.Error())
	default:
		h.failActionWatchError(w, r, err)
	}
}
