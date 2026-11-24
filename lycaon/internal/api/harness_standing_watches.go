package api

import (
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/watch"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type harnessStandingWatchReq struct {
	Action       string                   `json:"action"`
	ProjectID    string                   `json:"project_id"`
	WatchID      string                   `json:"watch_id,omitempty"`
	SessionID    string                   `json:"session_id,omitempty"`
	Handle       string                   `json:"handle,omitempty"`
	BaseRevision int                      `json:"base_revision,omitempty"`
	Cursor       string                   `json:"cursor,omitempty"`
	Limit        int                      `json:"limit,omitempty"`
	EveryMinutes int64                    `json:"every_minutes,omitempty"`
	Input        watch.StandingWatchInput `json:"input,omitempty"`
	Patch        watch.StandingWatchPatch `json:"patch,omitempty"`
}

func (s *HarnessControl) handleHarnessStandingWatches(w http.ResponseWriter, r *http.Request) {
	if s.watches == nil || s.watches.Standing() == nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "standing watch store unavailable")
		return
	}
	var req harnessStandingWatchReq
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	ctx := r.Context()
	engine := s.watches.Standing()
	owner, err := s.peopleStore.HostOwner(ctx)
	if err != nil {
		owner = people.Person{ID: "owner", Role: wire.PersonRoleOwner}
	}
	switch req.Action {
	case "create":
		req.Input.ProjectID = req.ProjectID
		sw, err := engine.Create(ctx, owner.ID, req.Input)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		httpio.WriteJSON(w, http.StatusOK, sw)
	case "keep":
		sw, rec, err := engine.KeepAgentWatch(ctx, owner.ID, req.SessionID, req.Handle, req.Input, req.BaseRevision)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		httpio.WriteJSON(w, http.StatusOK, map[string]any{"watch": sw, "receipt": rec})
	case "cancel_agent":
		rec, err := engine.CancelByPerson(ctx, req.SessionID, req.Handle)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		httpio.WriteJSON(w, http.StatusOK, rec)
	case "update":
		sw, err := engine.Update(ctx, owner.ID, req.ProjectID, req.WatchID, req.BaseRevision, req.Patch)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		httpio.WriteJSON(w, http.StatusOK, sw)
	case "pause":
		sw, err := engine.Pause(ctx, req.ProjectID, req.WatchID, req.BaseRevision)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		httpio.WriteJSON(w, http.StatusOK, sw)
	case "resume":
		sw, err := engine.Resume(ctx, owner.ID, req.ProjectID, req.WatchID, req.BaseRevision)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		httpio.WriteJSON(w, http.StatusOK, sw)
	case "delete":
		if err := engine.Delete(ctx, req.ProjectID, req.WatchID); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		httpio.WriteJSON(w, http.StatusOK, map[string]any{"deleted": true})
	case "run_now":
		run, err := engine.RunNow(ctx, owner.ID, req.ProjectID, req.WatchID)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		httpio.WriteJSON(w, http.StatusOK, run)
	case "list_runs":
		runs, next, err := engine.ListRuns(ctx, req.ProjectID, req.WatchID, req.Cursor, req.Limit)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		httpio.WriteJSON(w, http.StatusOK, map[string]any{"runs": runs, "next_cursor": next})
	case "provenance":
		prov, found, exists, err := engine.LookupChatProvenance(ctx, req.SessionID)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		httpio.WriteJSON(w, http.StatusOK, map[string]any{
			"provenance": prov, "found": found, "watch_exists": exists,
			"summary": watch.FormatEveryMinutesSummary(req.EveryMinutes),
		})
	default:
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "unknown harness standing-watch action")
	}
}
