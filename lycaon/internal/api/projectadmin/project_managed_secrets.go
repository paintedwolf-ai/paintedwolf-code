package projectadmin

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/api/secretview"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/people/personactions"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type secretUsePosition struct {
	UsedAt string `json:"used_at"`
	Index  int    `json:"index"`
}

var (
	secretUseBounds = httpio.MustPageLimit(50, 1, 100)
	secretUsePages  = pagecursor.For[secretUsePosition]("managed_secret_uses")
)

func (s *Handler) secretTarget(w http.ResponseWriter, r *http.Request) (*project.Project, bool) {
	p, ok := requestscope.ProjectByURLID(s.Registry, s.responses, w, r)
	if !ok {
		return nil, false
	}
	return p, true
}

// Routes use IDs while the service accepts full references.
func secretReference(r *http.Request) string {
	return secretmatch.ReferenceToken(strings.TrimSpace(chi.URLParam(r, "secret_id")))
}

func (s *Handler) HandleListProjectManagedSecrets(w http.ResponseWriter, r *http.Request) {
	p, ok := s.secretTarget(w, r)
	if !ok {
		return
	}
	items, err := s.ManagedSecrets.ListProject(r.Context(), p.ID)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	out := make([]wire.ManagedSecret, 0, len(items))
	for _, item := range items {
		out = append(out, secretview.Metadata(item))
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ManagedSecretList{Secrets: out})
}

func (s *Handler) HandleCreateProjectManagedSecret(w http.ResponseWriter, r *http.Request) {
	var body wire.CreateManagedSecretRequest
	if err := httpio.DecodeJSON(w, r, &body); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	p, ok := s.secretTarget(w, r)
	if !ok {
		return
	}
	operationID := strings.TrimSpace(body.OperationID)
	if _, err := uuid.Parse(operationID); err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "operation_id must be a valid uuid")
		return
	}
	meta, err := s.ManagedSecrets.CreateSettingsSecret(r.Context(), secretcap.CreateSettingsSecretRequest{
		ProjectID: p.ID, OperationID: operationID, PersonID: requestscope.Caller(r).ID,
		Name: strings.TrimSpace(body.Name), Purpose: strings.TrimSpace(body.Purpose),
		Value: body.SecretValue, AgentUseEndsAt: strings.TrimSpace(body.AgentUseEndsAt),
	})
	if err != nil {
		secretview.WriteError(s.responses, w, r, err)
		return
	}
	personactions.Note(r.Context(), "secret_reference", meta.Reference)
	httpio.WriteJSON(w, http.StatusCreated, secretview.Metadata(meta))
}

func (s *Handler) HandleUpdateProjectManagedSecret(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	var body wire.UpdateManagedSecretRequest
	if err := httpio.DecodeJSONWithRaw(w, r, &body, &raw); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	p, ok := s.secretTarget(w, r)
	if !ok {
		return
	}
	req := secretcap.UpdateRequest{
		ProjectID: p.ID, Reference: secretReference(r),
		Name: body.Name, Purpose: body.Purpose, Scope: body.Scope,
	}
	if _, ok := raw["agent_use_ends_at"]; ok {
		if body.AgentUseEndsAt == nil {
			req.AgentUseDeadline = &secretcap.AgentUseDeadline{At: ""}
		} else {
			req.AgentUseDeadline = &secretcap.AgentUseDeadline{At: body.AgentUseEndsAt.UTC().Format(time.RFC3339Nano)}
		}
	}
	meta, err := s.ManagedSecrets.Update(r.Context(), req)
	if err != nil {
		secretview.WriteError(s.responses, w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, secretview.Metadata(meta))
}

func (s *Handler) HandleReplaceProjectManagedSecretValue(w http.ResponseWriter, r *http.Request) {
	var body wire.ReplaceManagedSecretValueRequest
	if err := httpio.DecodeJSON(w, r, &body); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	p, ok := s.secretTarget(w, r)
	if !ok {
		return
	}
	meta, err := s.ManagedSecrets.ReplaceValue(r.Context(), secretcap.ReplaceValueRequest{
		ProjectID: p.ID, Reference: secretReference(r), Value: body.SecretValue,
	})
	if err != nil {
		secretview.WriteError(s.responses, w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, secretview.Metadata(meta))
}

func (s *Handler) HandleRevokeProjectManagedSecret(w http.ResponseWriter, r *http.Request) {
	p, ok := s.secretTarget(w, r)
	if !ok {
		return
	}
	_, err := s.ManagedSecrets.RevokeProject(r.Context(), p.ID, secretReference(r), requestscope.Caller(r).ID)
	if err != nil {
		secretview.WriteError(s.responses, w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Handler) HandleListProjectManagedSecretUses(w http.ResponseWriter, r *http.Request) {
	p, ok := s.secretTarget(w, r)
	if !ok {
		return
	}
	query, err := httpio.ReadPageQuery(r, secretUseBounds)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	history, err := s.ManagedSecrets.Uses(r.Context(), p.ID, secretReference(r), 0)
	if err != nil {
		secretview.WriteError(s.responses, w, r, err)
		return
	}
	startIndex := 0
	if query.Cursor != "" {
		pos, err := secretUsePages.Decode(query.Cursor, "")
		if err != nil {
			s.responses.PageCursorError(w, r, "cursor", err)
			return
		}
		startIndex = pos.Index
		if startIndex < 0 || startIndex > len(history.Items) {
			startIndex = len(history.Items)
		}
	}
	endIndex := startIndex + query.Limit
	hasMore := false
	if endIndex < len(history.Items) {
		hasMore = true
	} else {
		endIndex = len(history.Items)
	}
	var pageSlice []secretcap.Use
	if startIndex < len(history.Items) {
		pageSlice = history.Items[startIndex:endIndex]
	}
	out := make([]wire.ManagedSecretUse, 0, len(pageSlice))
	for _, use := range pageSlice {
		out = append(out, wire.ManagedSecretUse{
			UsedAt: use.UsedAt, ToolName: use.ToolName, Outcome: use.Outcome,
			ToolCallID: use.ToolCallID, Delivery: use.Delivery,
			Version: use.Version, SessionID: use.SessionID, ChatSessionID: use.ChatSessionID,
		})
	}
	var nextCursor string
	if hasMore && len(pageSlice) > 0 {
		last := &pageSlice[len(pageSlice)-1]
		token, err := secretUsePages.Encode("", secretUsePosition{
			UsedAt: last.UsedAt,
			Index:  endIndex,
		})
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		nextCursor = token
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ManagedSecretUseList{Uses: out, NextCursor: nextCursor})
}

func (s *Handler) HandleBeginProjectManagedSecretReveal(w http.ResponseWriter, r *http.Request) {
	var body wire.BeginManagedSecretRevealRequest
	if err := httpio.DecodeJSON(w, r, &body); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if !secretcap.IsRevealWindowLabel(strings.TrimSpace(body.WindowLabel)) {
		s.responses.InvalidField(w, "window_label", "must be 1 to 120 characters without control characters")
		return
	}
	p, ok := s.secretTarget(w, r)
	if !ok {
		return
	}
	challenge, err := s.ManagedSecrets.BeginReveal(
		r.Context(), p.ID, secretReference(r), strings.TrimSpace(body.WindowLabel), requestscope.Caller(r).ID,
	)
	if err != nil {
		secretview.WriteError(s.responses, w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusCreated, wire.ManagedSecretRevealChallenge{
		ChallengeID: challenge.ID, ProofPayload: challenge.ProofPayload,
		Prompt: challenge.Prompt, Version: challenge.Version, ExpiresAt: challenge.ExpiresAt,
	})
}

func (s *Handler) HandleCompleteProjectManagedSecretReveal(w http.ResponseWriter, r *http.Request) {
	var body wire.CompleteManagedSecretRevealRequest
	if err := httpio.DecodeJSON(w, r, &body); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	switch {
	case !secretcap.IsRevealAuthenticator(strings.TrimSpace(body.Authenticator)):
		s.responses.InvalidField(w, "authenticator", "must name a supported user-presence authenticator")
		return
	case strings.TrimSpace(body.Signature) == "":
		s.responses.InvalidField(w, "signature", "is required")
		return
	}
	p, ok := s.secretTarget(w, r)
	if !ok {
		return
	}
	result, err := s.ManagedSecrets.CompleteReveal(
		r.Context(), p.ID, secretReference(r), strings.TrimSpace(chi.URLParam(r, "challenge_id")),
		requestscope.Caller(r).ID, strings.TrimSpace(body.Authenticator), strings.TrimSpace(body.Signature),
	)
	if err != nil {
		secretview.WriteError(s.responses, w, r, err)
		return
	}
	remaskMs := result.RemaskAfterSeconds * 1000
	if remaskMs < 5000 {
		remaskMs = 5000
	} else if remaskMs > 60000 {
		remaskMs = 60000
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ManagedSecretRevealResponse{
		SecretValue: result.Value.Value(), Version: result.Version, RevealedAt: result.RevealedAt,
		RemaskAfterMs: remaskMs,
	})
}
