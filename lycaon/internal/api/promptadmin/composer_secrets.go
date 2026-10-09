package promptadmin

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/api/secretview"
	"github.com/lycaon/lycaon/internal/people/personactions"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/session"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Submission) HandleCreateComposerSecret(w http.ResponseWriter, r *http.Request) {
	var body wire.CreateComposerSecretRequest
	if err := httpio.DecodeJSON(w, r, &body); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	operationID, err := uuid.Parse(strings.TrimSpace(body.OperationID))
	if err != nil {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "operation_id must be a UUID")
		return
	}
	sessionID := strings.TrimSpace(chi.URLParam(r, "id"))
	sess, ok := requestscope.Session(s.Store, s.responses, w, r, sessionID)
	if !ok {
		return
	}
	chatSessionID := session.RootSessionID(r.Context(), s.Store, sessionID)
	put, err := s.ManagedSecrets.Put(r.Context(), secretcap.PutRequest{
		ProjectID: sess.ProjectID, ChatSessionID: chatSessionID, SessionID: sessionID,
		OperationID: operationID.String(), Name: strings.TrimSpace(body.Name),
		Purpose: strings.TrimSpace(body.Purpose), Scope: secretcap.ScopeChat,
		Origin: secretcap.OriginComposerMarked, Value: body.SecretValue, PersonID: requestscope.Caller(r).ID,
	})
	if err != nil {
		secretview.WriteError(s.responses, w, r, err)
		return
	}
	personactions.Note(r.Context(), "secret_reference", put.Metadata.Reference)
	httpio.WriteJSON(w, http.StatusCreated, secretview.Metadata(put.Metadata))
}
