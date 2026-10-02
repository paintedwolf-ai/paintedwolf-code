package capabilityadmin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/projectview"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/api/secretview"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleListCheckpoints(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	if !requestscope.SessionExists(s.Store, s.responses, w, r, sessionID) {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "" {
		status = string(wire.CheckpointStatusPending)
	} else if status != string(wire.CheckpointStatusPending) {
		s.responses.InvalidQueryParam(w, "status", "unsupported status filter")
		return
	}
	includeChildren := r.URL.Query().Get("include_children")
	if includeChildren != "" && includeChildren != "true" && includeChildren != "false" {
		s.responses.InvalidQueryParam(w, "include_children", "include_children must be true or false")
		return
	}
	kindFilter := strings.TrimSpace(r.URL.Query().Get("kind"))
	var kindPtr *wire.CheckpointKind
	if kindFilter != "" {
		switch wire.CheckpointKind(kindFilter) {
		case wire.CheckpointKindToolApproval, wire.CheckpointKindContentApply:
			k := wire.CheckpointKind(kindFilter)
			kindPtr = &k
		default:
			s.responses.InvalidQueryParam(w, "kind", "unsupported checkpoint kind")
			return
		}
	}
	switch wire.CheckpointStatus(status) {
	case wire.CheckpointStatusPending:
		read := s.Checkpoints.ListPending
		if includeChildren == "true" {
			read = s.Checkpoints.ListPendingForParent
		}
		pending, err := read(r.Context(), sessionID, kindPtr)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		out := make([]wire.CheckpointEvent, 0, len(pending))
		out = append(out, pending...)
		httpio.WriteJSON(w, http.StatusOK, wire.CheckpointListResponse{Checkpoints: out})
	default:
		s.responses.InvalidQueryParam(w, "status", "unsupported status filter")
	}
}

func (s *Handler) HandleResolveCheckpoint(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	if !requestscope.SessionExists(s.Store, s.responses, w, r, sessionID) {
		return
	}
	checkpointID := chi.URLParam(r, "checkpoint_id")
	var req wire.ResolveCheckpointRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	req.Guidance = observability.RedactCaptureText(req.Guidance)
	secretBlock, err := secretview.ReferenceBlock(s.Store, s.ManagedSecrets, r.Context(), sessionID, req.Secrets)
	if err != nil {
		secretview.WriteError(s.responses, w, r, err)
		return
	}
	req.Guidance = strings.TrimSpace(strings.Join([]string{req.Guidance, secretBlock}, "\n"))
	toolResult, contentResult, fieldErr := resolveFromRequest(req)
	if fieldErr != nil {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": fieldErr.field}, fieldErr.message)
		return
	}
	if req.Kind == wire.CheckpointKindToolApproval && req.Action == wire.ApprovalActionApprove {
		if strings.TrimSpace(req.OptionID) == "" {
			s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "option_id is required to approve a tool approval")
			return
		}
		resolver := hitl.HumanApproval()
		if req.Presence != nil {
			resolver = hitl.AttestedApproval(presence.Proof{
				ChallengeID: req.Presence.ChallengeID, Authenticator: string(req.Presence.Authenticator),
				Signature: req.Presence.Signature,
			})
		}
		resp, err := s.options.ResolveApprovalOptionBy(r.Context(), sessionID, checkpointID, req.OptionID, resolver)
		if err != nil {
			s.writeCheckpointResolveError(w, r, err)
			return
		}
		if resp != nil && resp.Result != nil && len(resp.Result.GrantIDs) > 0 {
			projectview.PublishSettings(s.Events, s.Projects, r.Context(), wire.SettingsAreaApprovals, string(wire.SettingsScopeGlobal), "", "updated")
		}
		httpio.WriteJSON(w, http.StatusOK, wireCheckpointResponse(sessionID, resp))
		return
	}
	resp, err := s.Checkpoints.ResolveCheckpoint(r.Context(), sessionID, checkpointID, req.Kind, toolResult, contentResult)
	if err != nil {
		s.writeCheckpointResolveError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wireCheckpointResponse(sessionID, resp))
}

// HandleBeginReleaseChallenge binds presence to one option that releases
// values a person holds. Only the desktop shell can answer it.
func (s *Handler) HandleBeginReleaseChallenge(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	if !requestscope.SessionExists(s.Store, s.responses, w, r, sessionID) {
		return
	}
	var body wire.BeginReleaseChallengeRequest
	if err := httpio.DecodeJSON(w, r, &body); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if !presence.IsWindowLabel(strings.TrimSpace(body.WindowLabel)) {
		s.responses.InvalidField(w, "window_label", "must be 1 to 120 characters without control characters")
		return
	}
	challenge, err := s.options.BeginReleaseChallenge(r.Context(), sessionID, chi.URLParam(r, "checkpoint_id"),
		strings.TrimSpace(body.OptionID), strings.TrimSpace(body.WindowLabel))
	if err != nil {
		s.writeCheckpointResolveError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusCreated, wire.PresenceChallenge{
		ChallengeID: challenge.ID, ProofPayload: challenge.ProofPayload, Prompt: challenge.Prompt,
		ExpiresAt: challenge.ExpiresAt,
	})
}

func (s *Handler) writeCheckpointResolveError(w http.ResponseWriter, r *http.Request, err error) {
	if secretview.WritePresenceError(s.responses, w, err) {
		return
	}
	switch {
	case errors.Is(err, hitl.ErrPresenceRequired):
		s.responses.Fail(w, wire.ApiErrorCodePresenceRequired, "this option releases a value you gave Painted Wolf Code; confirm it in the desktop app")
	case errors.Is(err, hitl.ErrPresenceNotRequired):
		s.responses.Fail(w, wire.ApiErrorCodePresenceNotRequired, "this option releases no value that needs your confirmation")
	case errors.Is(err, hitl.ErrCheckpointNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeCheckpointNotFound, "checkpoint not found")
	case errors.Is(err, hitl.ErrCheckpointNotPending):
		s.responses.Fail(w, wire.ApiErrorCodeCheckpointNotPending, "checkpoint is no longer pending")
	case errors.Is(err, lifecycle.ErrStopping):
		s.responses.Fail(w, wire.ApiErrorCodeSessionStopping, "the session is stopping")
	case errors.Is(err, hitl.ErrCheckpointKindMismatch):
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "kind"}, "kind does not match the checkpoint")
	case errors.Is(err, hitl.ErrApprovalOptionNotFound), errors.Is(err, hitl.ErrApprovalOptionUnavailable), errors.Is(err, hitl.ErrApprovalOptionRequired):
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "option_id"}, "option_id is not an available option for this approval")
	case errors.Is(err, hitl.ErrContentApplyHunkNotFound), errors.Is(err, hitl.ErrContentApplySelectionInvalid):
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "approved_hunks"}, "approved_hunks does not match the proposed change")
	default:
		s.responses.InternalError(w, r, err)
	}
}

// resolveFieldError names the request field a resolution refusal is about.
type resolveFieldError struct {
	field, message string
}

func resolveFromRequest(req wire.ResolveCheckpointRequest) (*hitl.DecisionResult, *hitl.ContentApplyResolve, *resolveFieldError) {
	guidance := strings.TrimSpace(req.Guidance)
	if guidance != "" && !guidanceValid(req) {
		return nil, nil, &resolveFieldError{"guidance", "guidance is only valid with a rejecting resolution"}
	}
	if req.OptionID != "" && (req.Kind != wire.CheckpointKindToolApproval || req.Action != wire.ApprovalActionApprove) {
		return nil, nil, &resolveFieldError{"option_id", "option_id requires an approving tool_approval resolution"}
	}
	if req.Presence != nil && (req.Kind != wire.CheckpointKindToolApproval || req.Action != wire.ApprovalActionApprove) {
		return nil, nil, &resolveFieldError{"presence", "presence accompanies an approving tool_approval resolution"}
	}
	switch req.Kind {
	case wire.CheckpointKindToolApproval:
		switch req.Action {
		case wire.ApprovalActionApprove:
			return &hitl.DecisionResult{Approved: true}, nil, nil
		case wire.ApprovalActionReject:
			return &hitl.DecisionResult{Approved: false, Comments: guidance}, nil, nil
		default:
			return nil, nil, &resolveFieldError{"action", "action must be approve or reject"}
		}
	case wire.CheckpointKindContentApply:
		decision := wire.ContentApplyDecision(req.Decision)
		switch decision {
		case wire.ContentApplyApprove:
			if len(req.ApprovedHunks) != 0 {
				return nil, nil, &resolveFieldError{"approved_hunks", "approved_hunks requires approve_partial"}
			}
		case wire.ContentApplyApprovePartial:
			if len(req.ApprovedHunks) == 0 {
				return nil, nil, &resolveFieldError{"approved_hunks", "approved_hunks is required for approve_partial"}
			}
		case wire.ContentApplyReject:
			if len(req.ApprovedHunks) != 0 {
				return nil, nil, &resolveFieldError{"approved_hunks", "approved_hunks requires approve_partial"}
			}
		default:
			return nil, nil, &resolveFieldError{"decision", "decision must be approve, approve_partial, or reject"}
		}
		return nil, &hitl.ContentApplyResolve{
			Decision:      decision,
			ApprovedHunks: req.ApprovedHunks,
			Guidance:      guidance,
		}, nil
	default:
		return nil, nil, &resolveFieldError{"kind", "kind is not a supported checkpoint kind"}
	}
}

// guidanceValid reports whether this resolution is the rejecting branch for its
// kind — the only place composer guidance may ride along.
func guidanceValid(req wire.ResolveCheckpointRequest) bool {
	switch req.Kind {
	case wire.CheckpointKindToolApproval:
		return req.Action == wire.ApprovalActionReject
	case wire.CheckpointKindContentApply:
		return wire.ContentApplyDecision(req.Decision) == wire.ContentApplyReject
	default:
		return false
	}
}

func wireCheckpointResponse(sessionID string, resp *hitl.CheckpointResponse) wire.CheckpointResponse {
	out := wire.CheckpointResponse{
		ID:         resp.CheckpointID,
		SessionID:  sessionID,
		Kind:       resp.Kind,
		Status:     wire.CheckpointStatus(resp.Status),
		ResolvedAt: resp.ResolvedAt,
	}
	if resp.Result != nil {
		out.Result = map[string]any{"approved": resp.Result.Approved}
		// Denial results expose direction as guidance for every checkpoint kind.
		if resp.Result.Comments != "" {
			out.Result["guidance"] = resp.Result.Comments
		}
		if resp.Result.RedactSecrets {
			out.Result["redacted"] = true
		}
		if len(resp.Result.GrantIDs) > 0 {
			out.Result["grant_ids"] = append([]string(nil), resp.Result.GrantIDs...)
			out.Result["grant_scope"] = resp.Result.GrantScope
			out.Result["grant_title"] = resp.Result.GrantTitle
		}
	}
	if resp.ContentResult != nil {
		if out.Result == nil {
			out.Result = map[string]any{}
		}
		out.Result["decision"] = resp.ContentResult.Decision
		if resp.ContentResult.Guidance != "" {
			out.Result["guidance"] = resp.ContentResult.Guidance
		}
	}
	return out
}
