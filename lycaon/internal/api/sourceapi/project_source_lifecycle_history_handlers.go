package sourceapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Mutations) HandleGetProjectSourceHistory(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	state, err := s.SourceMutations.History(r.Context(), p.ID)
	if err != nil {
		s.Workspace.writeSourceReadError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, toSourceHistoryStateDTO(state))
}

func (s *Mutations) HandleUndoProjectSourceHistory(w http.ResponseWriter, r *http.Request) {
	s.handleProjectSourceHistoryMutation(w, r, "undo")
}

func (s *Mutations) HandleRedoProjectSourceHistory(w http.ResponseWriter, r *http.Request) {
	s.handleProjectSourceHistoryMutation(w, r, "redo")
}

func (s *Mutations) handleProjectSourceHistoryMutation(w http.ResponseWriter, r *http.Request, direction string) {
	p, release, ok := s.beginProjectSourceMutation(w, r)
	if !ok {
		return
	}
	defer release()
	var req wire.SourceHistoryMutationRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	operationID, err := uuid.Parse(strings.TrimSpace(req.OperationID))
	if err != nil {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "operation_id must be a UUID")
		return
	}
	if _, err := uuid.Parse(strings.TrimSpace(req.ExpectedEntryID)); err != nil {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "expected_entry_id must be a UUID")
		return
	}
	sessionID, turn := s.Workspace.UserSourceChatAffiliation(r)
	var retargetID string
	historyReq := project.SourceHistoryMutationRequest{
		ExpectedEntryID: strings.TrimSpace(req.ExpectedEntryID),
		SessionID:       sessionID,
		Turn:            turn,
		Prepare: func(ctx context.Context, plan project.SourceRenamePlan) error {
			var prepareErr error
			retargetID, prepareErr = s.EditorDocuments.PrepareRetarget(ctx, p, plan)
			return prepareErr
		},
	}
	var result *project.SourceHistoryMutationResult
	if direction == "redo" {
		result, err = s.SourceMutations.Redo(r.Context(), operationID.String(), p, historyReq)
	} else {
		result, err = s.SourceMutations.Undo(r.Context(), operationID.String(), p, historyReq)
	}
	retargetErr := s.reconcileSourceHistoryRetarget(r.Context(), retargetID, err == nil)
	if err != nil {
		if retargetErr != nil {
			s.responses.Logger.ErrorContext(r.Context(), "reconcile editor documents after source history failure", "error", retargetErr)
		}
		s.Workspace.WriteProjectSourceError(w, r, err)
		return
	}
	if retargetErr != nil {
		s.responses.InternalError(w, r, retargetErr)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.SourceHistoryMutationResponse{
		EntryID: result.EntryID, RootID: result.RootID, Path: result.Path,
		FromPath: result.FromPath, Op: wire.SourceChangeOp(result.Op), IsDir: result.IsDir,
	})
}

func (s *Mutations) reconcileSourceHistoryRetarget(ctx context.Context, retargetID string, committed bool) error {
	var err error
	if retargetID != "" {
		err = s.EditorDocuments.ReconcileRetarget(ctx, retargetID)
	}
	if committed {
		err = errors.Join(err, s.EditorDocuments.ReconcilePendingRetargets(ctx))
	}
	return err
}

func toSourceHistoryStateDTO(state project.SourceHistoryState) wire.SourceHistoryState {
	return wire.SourceHistoryState{
		Undo: toSourceHistoryActionDTO(state.Undo),
		Redo: toSourceHistoryActionDTO(state.Redo),
	}
}

func toSourceHistoryActionDTO(action *project.SourceHistoryAction) *wire.SourceHistoryAction {
	if action == nil {
		return nil
	}
	return &wire.SourceHistoryAction{
		ID: action.ID, Label: action.Label, Kind: action.Kind, RootID: action.RootID,
		Path: action.Path, FromPath: action.FromPath, ToPath: action.ToPath,
		IsDir: action.IsDir, RemovesPath: action.RemovesPath,
	}
}
