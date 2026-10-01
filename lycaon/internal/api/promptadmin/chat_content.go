package promptadmin

import (
	"errors"
	"net/http"
	"strconv"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const chatContentPageRunes = 8192
const chatContentMaxRunes = 16384

var chatContentSearchLimit = httpio.MustPageLimit(50, 1, 500)

// chatContentSearchPages seals the rune position a search resumes from.
var chatContentSearchPages = pagecursor.For[int]("chat_content_search")

type chatContentRead struct {
	reference wire.ChatContentReference
	text      []rune
	spans     []wire.RedactedSpan
}

// readChatContent loads the addressed content. The caller checks the
// revision the client names with chatContentCurrent once its own query
// inputs are valid.
func (s *Handler) readChatContent(w http.ResponseWriter, r *http.Request) (chatContentRead, bool) {
	var out chatContentRead
	query := r.URL.Query()
	sessionID := chi.URLParam(r, "id")
	if _, err := s.Store.Get(r.Context(), sessionID); err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "chat not found")
			return out, false
		}
		s.responses.InternalError(w, r, err)
		return out, false
	}
	msg, err := s.Store.GetMessage(r.Context(), sessionID, chi.URLParam(r, "message_id"))
	if errors.Is(err, store.ErrMessageNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeChatContentNotFound, "Chat content not found.")
		return out, false
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return out, false
	}
	field, callID := query.Get("field"), query.Get("tool_call_id")
	text, spans, err := messageview.RetainedContent(msg, field, callID)
	if errors.Is(err, messageview.ErrContentNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeChatContentNotFound, "Chat content not found.")
		return out, false
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return out, false
	}
	ref := messageview.ContentReference(field, callID, text)
	return chatContentRead{reference: ref, text: []rune(text), spans: spans}, true
}

// chatContentCurrent refuses a read of a revision other than the one the
// client names in `sha256`.
func (s *Handler) chatContentCurrent(w http.ResponseWriter, r *http.Request, read chatContentRead) bool {
	if r.URL.Query().Get("sha256") != read.reference.SHA256 {
		s.responses.Fail(w, wire.ApiErrorCodeChatContentChanged, "This content changed. Reopen it to read the current version.")
		return false
	}
	return true
}

func (s *Handler) HandleGetChatContent(w http.ResponseWriter, r *http.Request) {
	read, ok := s.readChatContent(w, r)
	if !ok || !s.chatContentCurrent(w, r, read) {
		return
	}
	row, rowPresent, err := httpio.OptionalIntQuery(r, "row", 0, 0)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	locate, locatePresent, err := httpio.OptionalIntQuery(r, "locate", 0, 0)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	if rowPresent || locatePresent {
		s.writeChatContentRows(w, r, read, row, locate, locatePresent)
		return
	}
	offset, _, err := httpio.OptionalIntQuery(r, "offset", 0, 0)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	if offset > read.reference.TotalRunes {
		s.responses.InvalidQueryParam(w, "offset", "The content offset is outside this revision.")
		return
	}
	limit, present, err := httpio.OptionalIntQuery(r, "limit", 1, chatContentMaxRunes)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	if !present {
		limit = chatContentPageRunes
	}
	end := min(len(read.text), offset+limit)
	spans := make([]wire.RedactedSpan, 0)
	for _, span := range read.spans {
		start, stop := max(span.Start, offset), min(span.Start+span.Length, end)
		if start >= stop {
			continue
		}
		span.Field, span.Start, span.Length = "content", start-offset, stop-start
		spans = append(spans, span)
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ChatContentPage{Reference: read.reference, Text: string(read.text[offset:end]), Offset: offset, EndOffset: end, Complete: end == len(read.text), Spans: spans})
}

func (s *Handler) writeChatContentRows(w http.ResponseWriter, r *http.Request, read chatContentRead, row, locate int, locatePresent bool) {
	offsets := messageview.ContentRowOffsets(read.text)
	read.reference.Rows = len(offsets) - 1
	if locatePresent {
		row = messageview.ContentRowAt(offsets, locate)
	}
	if row > read.reference.Rows || locate > len(read.text) {
		s.responses.InvalidQueryParam(w, "row", "The content position is outside this revision.")
		return
	}
	rows := messageview.ContentRows(read.text, offsets, read.spans, row, 64, chatContentMaxRunes)
	end := row + len(rows)
	httpio.WriteJSON(w, http.StatusOK, wire.ChatContentPage{Reference: read.reference, Offset: offsets[row], EndOffset: offsets[end], Complete: end == read.reference.Rows, Rows: rows, Spans: []wire.RedactedSpan{}})
}

func (s *Handler) HandleSearchChatContent(w http.ResponseWriter, r *http.Request) {
	read, ok := s.readChatContent(w, r)
	if !ok {
		return
	}
	rawQuery := r.URL.Query().Get("q")
	query := []rune(rawQuery)
	if len(query) == 0 || len(query) > 1024 {
		s.responses.InvalidQueryParam(w, "q", "Search requires between 1 and 1024 characters.")
		return
	}
	sensitive, _, err := httpio.OptionalBoolQuery(r, "case_sensitive")
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	page, err := httpio.ReadPageQuery(r, chatContentSearchLimit)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	// The cursor is bound to the revision the client names.
	cursorScope := pagecursor.Scope(chi.URLParam(r, "id"), chi.URLParam(r, "message_id"), read.reference.Field,
		read.reference.ToolCallID, r.URL.Query().Get("sha256"), rawQuery, strconv.FormatBool(sensitive))
	start := 0
	if page.Cursor != "" {
		if start, err = chatContentSearchPages.Decode(page.Cursor, cursorScope); err != nil {
			s.responses.PageCursorError(w, r, "cursor", err)
			return
		}
	}
	if !s.chatContentCurrent(w, r, read) {
		return
	}
	if start < 0 || start > len(read.text) {
		s.responses.PageCursorError(w, r, "cursor", pagecursor.ErrInvalid)
		return
	}
	if !sensitive {
		for i := range query {
			query[i] = unicode.ToLower(query[i])
		}
	}
	matches := make([]wire.ChatContentMatch, 0)
	// Prefix fallback makes repeated-prefix searches linear in retained text size.
	prefix := make([]int, len(query))
	for i, matched := 1, 0; i < len(query); i++ {
		for matched > 0 && query[i] != query[matched] {
			matched = prefix[matched-1]
		}
		if query[i] == query[matched] {
			matched++
		}
		prefix[i] = matched
	}
	at, matched := start, 0
	for at < len(read.text) {
		if at%4096 == 0 {
			if r.Context().Err() != nil {
				return
			}
		}
		actual := read.text[at]
		if !sensitive {
			actual = unicode.ToLower(actual)
		}
		for matched > 0 && actual != query[matched] {
			matched = prefix[matched-1]
		}
		if actual == query[matched] {
			matched++
		}
		at++
		if matched != len(query) {
			continue
		}
		matches = append(matches, wire.ChatContentMatch{Offset: at - len(query), Length: len(query)})
		matched = 0
		if len(matches) == page.Limit {
			break
		}
	}
	out := wire.ChatContentSearchPage{Matches: matches}
	if at < len(read.text) {
		if out.NextCursor, err = chatContentSearchPages.Encode(cursorScope, at); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}
