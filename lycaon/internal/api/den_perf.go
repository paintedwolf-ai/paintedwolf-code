package api

import (
	"net/http"
	"time"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/observability"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const maxDenPerfEvents = 32

// handleDenPerfEvents appends Den client main-thread stall/perf lines to the
// den-perf.jsonl debug capture when LYCAON_DEN_PERF_DEBUG or LYCAON_DEBUG_ALL is
// on. When capture is off the handler still returns 204 so Den can fire-and-
// forget without treating disabled capture as an error.
func (s *Server) handleDenPerfEvents(w http.ResponseWriter, r *http.Request) {
	var req wire.DenPerfEventsRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if len(req.Events) == 0 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "events must be a non-empty array")
		return
	}
	if len(req.Events) > maxDenPerfEvents {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "too many events in one batch")
		return
	}
	if !observability.DenPerfDebugEnabled() {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	for _, ev := range req.Events {
		if ev.Event == "" {
			continue
		}
		entry := observability.DenPerfDebugEntry{
			Event:   ev.Event,
			Channel: ev.Channel,
			Detail:  ev.Detail,
		}
		if ts := ev.ObservedAt; ts != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				entry.Time = parsed.UTC()
			} else if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
				entry.Time = parsed.UTC()
			}
		}
		observability.LogDenPerfEvent(entry)
	}
	w.WriteHeader(http.StatusNoContent)
}
