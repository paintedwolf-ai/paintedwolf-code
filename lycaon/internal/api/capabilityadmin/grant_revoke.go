package capabilityadmin

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/projectview"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/people/personactions"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// revokeGrantByID removes every authority projection sharing the reviewed ID.
func (s *Handler) revokeGrantByID(ctx context.Context, id string) (bool, error) {
	stored, err := s.revokeStoredGrant(id)
	if err != nil {
		return false, err
	}
	runtime := s.revokeRuntimeGrant(ctx, id)
	return stored || runtime, nil
}

func (s *Handler) revokeRuntimeGrant(ctx context.Context, id string) bool {
	// Granted paths share one ID across runtime and lease projections.
	revoked := s.GrantedPaths != nil && s.GrantedPaths.RevokeByID(id)
	if s.revokeDirectIPGrant(ctx, id) {
		revoked = true
	}
	if s.ReadPaths != nil {
		if _, ok := s.ReadPaths.RevokeByID(id); ok {
			revoked = true
		}
	}
	if s.WriteRoots != nil {
		if _, _, ok := s.WriteRoots.FindByID(id); ok {
			_, _ = s.WriteRoots.RevokeByID(id)
			revoked = true
		}
	}
	if s.Listen != nil {
		if _, _, ok := s.Listen.FindByID(id); ok {
			_, _ = s.Listen.RevokeByID(id)
			revoked = true
		}
	}
	if s.Loopback != nil {
		if _, _, ok := s.Loopback.FindByID(id); ok {
			_, _ = s.Loopback.RevokeByID(id)
			revoked = true
		}
	}
	if s.Sockets != nil {
		if tg, root, ok := s.Sockets.FindByID(id); ok {
			_, _ = s.Sockets.RevokeByID(id)
			s.recordSocketRevoked(ctx, root, authzledger.CapabilitySocket{
				ApprovedPath: tg.ApprovedPath,
				ResolvedPath: tg.ResolvedPath,
				Scope:        "chat",
			})
			revoked = true
		}
	}

	return revoked
}

func (s *Handler) revokeStoredGrant(id string) (bool, error) {
	return s.Gate.RevokeGrant(id)
}

// maxBulkRevokeIDs bounds one bulk revoke request; the panel never holds more
// live rows than this.
const maxBulkRevokeIDs = 256

func (s *Handler) HandleRevokeApprovalGrants(w http.ResponseWriter, r *http.Request) {
	var req wire.RevokeApprovalGrantsRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if len(req.IDs) == 0 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "ids is required")
		return
	}
	if len(req.IDs) > maxBulkRevokeIDs {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "too many ids in one revoke")
		return
	}
	release := s.lockApprovalRevocation()
	defer release()
	results := make([]wire.ApprovalGrantRevokeResult, 0, len(req.IDs))
	for _, raw := range req.IDs {
		outcome := s.revokeApprovalRecord(r.Context(), strings.TrimSpace(raw))
		result := wire.ApprovalGrantRevokeResult{ID: outcome.ID, Revoked: outcome.Disposition == "revoked"}
		if outcome.Disposition == "failed" {
			result.Code = outcome.Code
			result.Message = outcome.Message
		}
		if outcome.Disposition == "already_absent" {
			result.Code = wire.ApiErrorCodeApprovalGrantNotFound
			result.Message = "grant not found"
		}
		results = append(results, result)
	}
	projectview.PublishSettings(s.Events, s.Projects, r.Context(), "approvals", string(llm.SettingsScopeGlobal), "", "updated")
	httpio.WriteJSON(w, http.StatusOK, wire.RevokeApprovalGrantsResponse{Results: results})
}

// revokeApprovalRecord removes persistence first so a failed durable removal
// remains visible and retryable, rather than resurrecting on restart.
func (s *Handler) revokeApprovalRecord(ctx context.Context, id string) wire.ElevatedAccessRevokeResult {
	result := wire.ElevatedAccessRevokeResult{ID: id, Disposition: "failed"}
	if !strings.HasPrefix(id, "grant_") && !strings.HasPrefix(id, "quiet_") {
		result.Code = wire.ApiErrorCodeInvalidRequest
		result.Message = "invalid grant id"
		return result
	}
	recorded, err := s.forgetChatGrant(ctx, id)
	if err != nil {
		slog.ErrorContext(ctx, "revoke approval record", "grant_id", id, "err", err)
		result.Code = wire.ApiErrorCodeInternalError
		result.Message = "the approval could not be revoked"
		return result
	}
	var found bool
	if strings.HasPrefix(id, "quiet_") {
		found = s.Gate.RevokeAskQuiet(id)
	} else {
		found, err = s.revokeGrantByID(ctx, id)
	}
	if err != nil {
		slog.ErrorContext(ctx, "revoke approval record", "grant_id", id, "err", err)
		result.Code = wire.ApiErrorCodeInternalError
		result.Message = "the approval could not be revoked"
		return result
	}
	result.Disposition = "already_absent"
	if found || recorded {
		result.Disposition = "revoked"
		personactions.Note(ctx, "revoked_grant_id", id)
	}
	return result
}

// forgetChatGrant removes a revoked chat grant so a restart does not restore it.
func (s *Handler) forgetChatGrant(ctx context.Context, id string) (bool, error) {
	if s.ChatGrants == nil {
		return false, nil
	}
	return s.ChatGrants.ForgetChatGrant(ctx, id)
}

func (s *Handler) recordSocketRevoked(ctx context.Context, chatSessionID string, socket authzledger.CapabilitySocket) {
	if s == nil || s.AuthzRecorder == nil {
		return
	}
	_ = s.AuthzRecorder.AppendCapabilityRecord(ctx, authzledger.CapabilityRecord{
		SessionID:           chatSessionID,
		Action:              authzledger.ActionCapabilityRevoked,
		Outcome:             authzledger.OutcomeAllowed,
		ResolvedBy:          authzledger.ResolvedByHuman,
		ResolverPersonID:    requestscope.ContextCaller(ctx).ID,
		Tool:                "settings",
		AuthorizationSource: authzledger.AuthorizationSourceHuman,
		Sockets:             []authzledger.CapabilitySocket{socket},
	})
}

func (s *Handler) lockApprovalRevocation() func() {
	var releaseLedger func()
	if owner, ok := s.ChatGrants.(interface{ LockApprovalAuthority() func() }); ok {
		releaseLedger = owner.LockApprovalAuthority()
	}
	s.authorityMu.Lock()
	return func() {
		s.authorityMu.Unlock()
		if releaseLedger != nil {
			releaseLedger()
		}
	}
}
