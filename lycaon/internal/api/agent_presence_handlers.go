package api

import (
	"context"
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/editordoc"
)

// handleGetAgentPresence returns what each chat is doing in the project's files.
func (s *Activity) handleGetAgentPresence(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.projectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.agentPresence.Snapshot(p.ID))
}

// EditorDocumentChanged schedules a presence staleness check after a content change.
func (s *Activity) EditorDocumentChanged(ctx context.Context, change editordoc.Change) {
	d := change.Document
	if change.ContentChanged {
		s.agentPresence.DocumentChanged(ctx, d.ProjectID, d.ID, d.Revision)
	}
}
