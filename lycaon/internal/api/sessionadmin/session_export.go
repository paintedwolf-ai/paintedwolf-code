package sessionadmin

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Transcript) HandleExportSessionTranscript(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "md"
	}
	if format != "md" && format != "json" {
		s.responses.Fail(w, wire.ApiErrorCodeExportFormatInvalid, "format must be md or json")
		return
	}

	sess, err := s.Store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "chat not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}

	page, err := s.loadFullTranscriptPage(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "chat not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	if page.Messages == nil {
		page.Messages = []wire.Message{}
	}

	filename := sanitizeTranscriptExportFilename(sess.Title, sess.ID, format)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)

	switch format {
	case "json":
		raw, err := json.Marshal(page)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	default:
		md := renderTranscriptMarkdown(page.Messages, transcriptMarkdownOpts{
			Title:      sess.Title,
			SessionID:  sess.ID,
			ExportedAt: time.Now().UTC(),
		})
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(md))
	}
}

// loadFullTranscriptPage walks GetTranscriptPage windows until the full
// ascending transcript is collected (same authority as GET .../messages).
func (s *Transcript) loadFullTranscriptPage(ctx context.Context, id string) (wire.SessionTranscriptPage, error) {
	q := wire.TranscriptPageQuery{Limit: wire.MaxTranscriptPageLimit}
	page, err := s.Sessions.Runner.Transcript.GetTranscriptPage(ctx, id, q)
	if err != nil {
		return wire.SessionTranscriptPage{}, err
	}
	out := wire.SessionTranscriptPage{
		Messages:   append([]wire.Message(nil), page.Messages...),
		TurnClocks: page.TurnClocks,
		TurnLoads:  page.TurnLoads,
		Watermark:  page.Watermark,
	}
	for page.BeforeCursor != "" {
		pos, err := store.MessagePages.Decode(page.BeforeCursor, pagecursor.Scope(id))
		if err != nil {
			return wire.SessionTranscriptPage{}, err
		}
		page, err = s.Sessions.Runner.Transcript.GetTranscriptPage(ctx, id, wire.TranscriptPageQuery{
			Limit:  wire.MaxTranscriptPageLimit,
			Before: &pos.Ord,
		})
		if err != nil {
			return wire.SessionTranscriptPage{}, err
		}
		out.Messages = append(append([]wire.Message(nil), page.Messages...), out.Messages...)
		maps.Copy(out.TurnClocks, page.TurnClocks)
		maps.Copy(out.TurnLoads, page.TurnLoads)
	}
	return out, nil
}
