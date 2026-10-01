package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/events"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// maxEventClientID bounds the client identity a stream may name.
const maxEventClientID = 100

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	projectID := strings.TrimSpace(query.Get("project_id"))
	after := strings.TrimSpace(query.Get("after"))
	clientID := strings.TrimSpace(query.Get("client_id"))
	if query.Has("project_id") && projectID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "project_id must be omitted for a device-wide stream")
		return
	}
	if projectID != "" {
		if _, err := s.projectRegistry.Get(r.Context(), projectID); err != nil {
			s.responses.Fail(w, wire.ApiErrorCodeProjectNotFound, "project not found")
			return
		}
	}
	if query.Has("client_id") && (clientID == "" || len(clientID) > maxEventClientID) {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "client_id must name the client this stream belongs to")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeInternalError, "streaming not supported")
		return
	}

	// An empty cursor subscribes from now; a cursor past the retained window
	// is a conflict carrying the current one to resubscribe from.
	ch, unsubscribe, err := s.events.Subscribe(r.Context(), events.Subscription{
		Project: projectID, Viewer: requestscope.Caller(r), After: after,
	})
	if err != nil {
		if errors.Is(err, events.ErrReplayUnavailable) {
			details := map[string]any{"event_cursor": s.events.CurrentCursor()}
			s.responses.FailDetails(w, wire.ApiErrorCodeEventReplayUnavailable, details, "event replay is no longer available")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	defer unsubscribe()

	// An editing lease lives only as long as its holder's stream.
	if s.Sources.EditorClients != nil && clientID != "" {
		defer s.Sources.EditorClients.Connected(clientID)()
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	if projectID != "" {
		if s.projectLiveness != nil {
			defer s.projectLiveness.ClaimWorkspace(projectID)()
		}
		s.Sources.ScheduleSourceWatch(r.Context(), projectID)
	}

	if _, err := fmt.Fprintf(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()

	heartbeat := events.HeartbeatTicker()
	defer heartbeat.Stop()

	shuttingDown := s.ShuttingDown()

	for {
		select {
		case <-shuttingDown:
			return
		case envelope, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(envelope)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprintf(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
