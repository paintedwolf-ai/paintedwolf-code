package api

import (
	"errors"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/visual"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Server) handleListSessionArtifacts(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	if _, err := s.sessionStore.Get(r.Context(), sessionID); err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "This chat no longer exists.")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	pq, err := httpio.ReadPageQuery(r, sessionArtifactPageLimit)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	root := session.RootSessionID(r.Context(), s.sessionStore, sessionID)
	scope := pagecursor.Scope(root)
	var after *sessionArtifactCursor
	if pq.Cursor != "" {
		cursor, err := sessionArtifactPages.Decode(pq.Cursor, scope)
		if err == nil && (cursor.CreatedAt.IsZero() || strings.TrimSpace(cursor.ID) == "") {
			err = pagecursor.ErrInvalid
		}
		if err != nil {
			s.responses.PageCursorError(w, r, "cursor", err)
			return
		}
		after = &cursor
	}
	items, err := s.visualStore.ListTree(r.Context(), root)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	page, next := sessionArtifactPage(items, after, pq.Limit)
	out := wire.ArtifactListResponse{Artifacts: page}
	if next != nil {
		if out.NextCursor, err = sessionArtifactPages.Encode(scope, *next); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

// sessionArtifactCursor resumes after the last artifact served, in
// (created_at, id) order.
type sessionArtifactCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

var (
	sessionArtifactPages     = pagecursor.For[sessionArtifactCursor]("session_artifacts")
	sessionArtifactPageLimit = httpio.MustPageLimit(50, 1, 500)
)

// sessionArtifactPage orders a session tree's artifacts oldest first and
// returns the page after the cursor, with the cursor for the next page when
// more remain.
func sessionArtifactPage(items []wire.ArtifactListItem, after *sessionArtifactCursor, limit int) ([]wire.ArtifactListItem, *sessionArtifactCursor) {
	ordered := slices.Clone(items)
	slices.SortStableFunc(ordered, func(a, b wire.ArtifactListItem) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	start := 0
	if after != nil {
		start = sort.Search(len(ordered), func(i int) bool {
			if c := ordered[i].CreatedAt.Compare(after.CreatedAt); c != 0 {
				return c > 0
			}
			return ordered[i].ID > after.ID
		})
	}
	end := min(start+limit, len(ordered))
	page := append([]wire.ArtifactListItem{}, ordered[start:end]...)
	if end == len(ordered) || len(page) == 0 {
		return page, nil
	}
	last := page[len(page)-1]
	return page, &sessionArtifactCursor{CreatedAt: last.CreatedAt, ID: last.ID}
}

func (s *Server) handleCreateSessionArtifact(w http.ResponseWriter, r *http.Request) {
	pageID := strings.TrimSpace(r.URL.Query().Get("page_id"))
	if pageID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "live tool recording page_id required")
		return
	}
	originMessageID := strings.TrimSpace(r.URL.Query().Get("assistant_message_id"))
	toolCallID := strings.TrimSpace(r.URL.Query().Get("tool_call_id"))
	if originMessageID == "" || toolCallID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "live tool recording transcript origin required")
		return
	}
	operationID, err := uuid.Parse(strings.TrimSpace(r.URL.Query().Get("operation_id")))
	if err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "live tool recording operation_id must be a UUID")
		return
	}
	sessionID := strings.TrimSpace(chi.URLParam(r, "id"))
	sess, err := s.sessionStore.Get(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "This chat no longer exists.")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	durationMS, ok := parseRecordingDuration(r.URL.Query().Get("duration_ms"))
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeRecordingDurationInvalid, "duration_ms must be a non-negative integer")
		return
	}
	recordedAt, ok := parseRecordingStart(r.URL.Query().Get("recorded_at"))
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeRecordingStartInvalid, "recorded_at must be RFC 3339")
		return
	}
	if err := httpio.RequireRequestMediaType(r, recordingMediaType); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if r.ContentLength > int64(visual.MaxVideoBytes) {
		s.responses.Fail(w, wire.ApiErrorCodeArtifactTooLarge, "live tool recording exceeds the video artifact limit")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(visual.MaxVideoBytes))
	raw, err := io.ReadAll(r.Body)
	if httpio.IsBodyTooLarge(err) {
		s.responses.Fail(w, wire.ApiErrorCodeArtifactTooLarge, "live tool recording exceeds the video artifact limit")
		return
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if len(raw) == 0 {
		s.responses.Fail(w, wire.ApiErrorCodeArtifactEmpty, "live tool recording bytes required")
		return
	}
	messages, err := s.sessionStore.GetMessages(r.Context(), sessionID)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if !recordingOriginExists(messages, originMessageID, toolCallID) {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "live tool recording transcript origin not found")
		return
	}
	root := session.RootSessionID(r.Context(), s.sessionStore, sess.ID)
	holder := strings.TrimSpace(sess.ID)
	artifact, err := s.visualStore.Put(r.Context(), root, visual.Entry{
		Meta: visual.LiveToolRecordingMeta(recordingMediaType, pageID, originMessageID, toolCallID, recordedAt, durationMS),
		// Recording storage follows the page-holder session.
		ProducerSessionID:  holder,
		OperationID:        operationID.String(),
		AutomaticRecording: true,
		NaturalKey:         liveRecordingSlot(root, holder, pageID, originMessageID, toolCallID),
		Bytes:              raw,
	})
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusCreated, artifact)
}

const recordingMediaType = "video/mp4"

func recordingOriginExists(messages []wire.Message, messageID, toolCallID string) bool {
	for _, message := range messages {
		if message.ID != messageID || message.Role != wire.MessageRoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.ID == toolCallID {
				return true
			}
		}
	}
	return false
}

func liveRecordingSlot(rootSessionID, holderSessionID, pageID, originMessageID, toolCallID string) string {
	return strings.Join([]string{
		"live-tool-recording",
		rootSessionID,
		holderSessionID,
		pageID,
		originMessageID,
		toolCallID,
	}, "/")
}

func parseRecordingDuration(raw string) (int64, bool) {
	if strings.TrimSpace(raw) == "" {
		return 0, true
	}
	durationMS, err := strconv.ParseInt(raw, 10, 64)
	return durationMS, err == nil && durationMS >= 0
}

func parseRecordingStart(raw string) (time.Time, bool) {
	if strings.TrimSpace(raw) == "" {
		return time.Now().UTC(), true
	}
	recordedAt, err := time.Parse(time.RFC3339, raw)
	return recordedAt.UTC(), err == nil
}

func (s *Server) handleListProjectArtifacts(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.projectRegistry, &s.responses, w, r)
	if !ok {
		return
	}
	pq, err := httpio.ReadPageQuery(r, artifactPageLimit)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	query, err := artifactPageQuery(pq, p.ID)
	if err != nil {
		s.responses.PageCursorError(w, r, "cursor", err)
		return
	}
	page, err := s.visualStore.ListProject(r.Context(), p.ID, query)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if page.Items == nil {
		page.Items = []wire.ArtifactListItem{}
	}
	nextCursor, err := encodeArtifactCursor(p.ID, page.NextCreatedAt, page.NextID)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ArtifactListResponse{
		Artifacts: page.Items, NextCursor: nextCursor,
	})
}

type artifactCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

var (
	artifactPages     = pagecursor.For[artifactCursor]("project_artifacts")
	artifactPageLimit = httpio.MustPageLimit(visual.DefaultArtifactPageLimit, 1, visual.MaxArtifactPageLimit)
)

func artifactPageQuery(pq httpio.PageQuery, projectID string) (visual.ArtifactPageQuery, error) {
	query := visual.ArtifactPageQuery{Limit: pq.Limit}
	if pq.Cursor == "" {
		return query, nil
	}
	cursor, err := artifactPages.Decode(pq.Cursor, pagecursor.Scope(projectID))
	if err != nil {
		return query, err
	}
	if strings.TrimSpace(cursor.CreatedAt) == "" || strings.TrimSpace(cursor.ID) == "" {
		return query, pagecursor.ErrInvalid
	}
	query.AfterCreatedAt, query.AfterID = cursor.CreatedAt, cursor.ID
	return query, nil
}

func encodeArtifactCursor(projectID, createdAt, id string) (string, error) {
	if strings.TrimSpace(createdAt) == "" || strings.TrimSpace(id) == "" {
		return "", nil
	}
	return artifactPages.Encode(pagecursor.Scope(projectID), artifactCursor{CreatedAt: createdAt, ID: id})
}

// Deletion preserves durable references after removing artifact bytes.
func (s *Server) handleDeleteProjectArtifact(w http.ResponseWriter, r *http.Request) {
	artifactID := strings.TrimSpace(chi.URLParam(r, "artifact_id"))
	if artifactID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "artifact id required")
		return
	}
	p, ok := requestscope.ProjectByURLID(s.projectRegistry, &s.responses, w, r)
	if !ok {
		return
	}
	_, found, err := s.visualStore.Delete(r.Context(), p.ID, artifactID, "human_delete")
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if !found {
		s.responses.Fail(w, wire.ApiErrorCodeArtifactNotFound, "no such artifact in this project")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSessionArtifact(w http.ResponseWriter, r *http.Request) {
	// Artifact identities survive deletion; bytes must be resolved on each fetch.
	w.Header().Set("Cache-Control", "private, no-store")
	sessionID := chi.URLParam(r, "id")
	artifactID := strings.TrimSpace(chi.URLParam(r, "artifact_id"))
	if artifactID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "artifact id required")
		return
	}
	if _, err := s.sessionStore.Get(r.Context(), sessionID); err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "This chat no longer exists.")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	root := session.RootSessionID(r.Context(), s.sessionStore, sessionID)
	res := s.visualStore.Resolve(r.Context(), root, artifactID)
	if !res.IsPresent() {
		switch res.Reason() {
		case visual.AbsenceForeign:
			s.responses.Fail(w, wire.ApiErrorCodeArtifactForeign, res.Note())
		case visual.AbsenceUnavailable:
			s.responses.Fail(w, wire.ApiErrorCodeArtifactUnavailable, res.Note())
		case visual.AbsenceDeleted:
			s.responses.Fail(w, wire.ApiErrorCodeArtifactDeleted, res.Note())
		default:
			s.responses.Fail(w, wire.ApiErrorCodeArtifactNotFound, res.Note())
		}
		return
	}
	contentType := strings.TrimSpace(res.Meta().Mime)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	// #nosec G705 -- stored visual bytes are the response body.
	_, _ = w.Write(res.Bytes())
}
