package app

import (
	"context"

	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/session/viewcontext"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/pkg/api"
)

// visibleEditorsAdapter tells a session which documents the sender's visible editors show.
type visibleEditorsAdapter struct {
	service *editordoc.Service
}

func (a visibleEditorsAdapter) VisibleEditors(ctx context.Context, projectID string, branch sourcebranch.ID, personID string, shown []viewcontext.ShownEditor) ([]api.ViewEditor, error) {
	editors := make([]editordoc.EditorShown, len(shown))
	for i, s := range shown {
		editors[i] = editordoc.EditorShown{ClientID: s.ClientID, DocumentID: s.DocumentID}
	}
	documents, err := a.service.VisibleDocuments(ctx, projectID, branch, personID, editors)
	if err != nil {
		return nil, err
	}
	out := make([]api.ViewEditor, len(documents))
	for i, d := range documents {
		out[i] = api.ViewEditor{RootID: d.RootID, Path: d.Path, Unsaved: d.Unsaved}
		if d.Selection != nil {
			out[i].Selection = &api.ViewSelection{StartLine: d.Selection.StartLine, EndLine: d.Selection.EndLine, Cursor: d.Selection.Empty}
		}
	}
	return out, nil
}
