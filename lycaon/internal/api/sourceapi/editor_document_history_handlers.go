package sourceapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/pagecursor"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type editorChangeCursor struct {
	BeforeRevision int64 `json:"before_revision"`
}

var (
	editorChangePages     = pagecursor.For[editorChangeCursor]("editor_document_changes")
	editorChangePageLimit = httpio.MustPageLimit(50, 1, 500)
)

func (s *Handler) HandleListEditorDocumentChanges(w http.ResponseWriter, r *http.Request) {
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, strings.TrimSpace(chi.URLParam(r, "id")))
	if release == nil {
		return
	}
	defer release()
	p, ok := s.editorProject(w, r)
	if !ok {
		return
	}
	documentID := chi.URLParam(r, "document_id")
	page, err := httpio.ReadPageQuery(r, editorChangePageLimit)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	scope := pagecursor.Scope(p.ID, documentID)
	var before int64
	if page.Cursor != "" {
		cursor, err := editorChangePages.Decode(page.Cursor, scope)
		if err == nil && cursor.BeforeRevision < 1 {
			err = pagecursor.ErrInvalid
		}
		if err != nil {
			s.responses.PageCursorError(w, r, "cursor", err)
			return
		}
		before = cursor.BeforeRevision
	}
	changes, err := s.EditorDocuments.Changes(r.Context(), p.ID, documentID, before, page.Limit+1)
	if err != nil {
		s.writeEditorDocumentError(w, r, err)
		return
	}
	more := len(changes) > page.Limit
	if more {
		changes = changes[:page.Limit]
	}
	response := wire.EditorDocumentChanges{Changes: make([]wire.EditorDocumentChange, 0, len(changes))}
	for _, change := range changes {
		createdAt, err := time.Parse(time.RFC3339Nano, change.CreatedAt)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		response.Changes = append(response.Changes, wire.EditorDocumentChange{
			Revision:     change.Revision,
			Epoch:        change.Epoch,
			OperationID:  change.OperationID,
			SessionID:    change.SessionID,
			SessionTitle: change.SessionTitle,
			Turn:         change.Turn,
			ActorKind:    change.ActorKind,
			PersonID:     change.PersonID,
			ClientID:     change.ClientID,
			RevertedBy:   change.RevertedBy,
			CreatedAt:    createdAt,
		})
	}
	if more {
		next, err := editorChangePages.Encode(scope, editorChangeCursor{BeforeRevision: changes[len(changes)-1].Revision})
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		response.NextCursor = next
	}
	httpio.WriteJSON(w, http.StatusOK, response)
}

func (s *Handler) HandleRevertEditorDocumentChange(w http.ResponseWriter, r *http.Request) {
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, strings.TrimSpace(chi.URLParam(r, "id")))
	if release == nil {
		return
	}
	defer release()
	p, ok := s.editorProject(w, r)
	if !ok {
		return
	}
	var req wire.RevertEditorDocumentChangeRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	sessionID, turn, ok := s.editorAffiliation(w, r, p.ID, &req.SessionID)
	if !ok {
		return
	}
	d, err := s.EditorDocuments.Revert(r.Context(), p.ID, chi.URLParam(r, "document_id"), editordoc.RevertChange{HistoryVector: req.HistoryVector, SessionID: sessionID, Turn: turn,
		ClientID: req.ClientID, OperationID: req.OperationID, ChangeOperationID: strings.TrimSpace(chi.URLParam(r, "change_id")), Epoch: req.Epoch})
	if err != nil {
		s.writeEditorDocumentError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.editorDocumentDTO(r.Context(), d))
}

func editorCommandHistoryDTO(history *editordoc.CommandHistory) *wire.EditorCommandHistory {
	if history == nil {
		return nil
	}
	return &wire.EditorCommandHistory{OperationID: history.OperationID, Epoch: history.Epoch, BeforeUpdate: history.BeforeUpdate, Update: history.Update}
}
