package capabilityadmin

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/projectview"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) elevatedAccessChat(w http.ResponseWriter, r *http.Request) (*wire.Session, bool) {
	id := chi.URLParam(r, "id")
	seen := make(map[string]bool)
	for len(seen) < 64 && !seen[id] {
		seen[id] = true
		chat, ok := requestscope.Session(s.Store, s.responses, w, r, id)
		if !ok {
			return nil, false
		}
		if chat.ParentSessionID == "" {
			return chat, true
		}
		id = chat.ParentSessionID
	}
	s.responses.InternalError(w, r, errors.New("chat lineage cannot be resolved"))
	return nil, false
}

func (s *Handler) elevatedAccessSummary(ctx context.Context, chat *wire.Session) (wire.ElevatedAccessSummary, error) {
	summary := wire.ElevatedAccessSummary{
		RootSessionID: chat.ID, ApprovalsEnabled: true,
		Records: []wire.ElevatedAccessRecord{}, SharedScopes: []wire.ApprovalGrantScope{},
	}
	enabled, err := s.elevatedApprovalsEnabled(ctx, chat)
	if err != nil {
		return summary, err
	}
	summary.ApprovalsEnabled = enabled
	if !enabled {
		return summary, nil
	}
	now := time.Now()
	action := hitl.ProposedAction{SessionID: chat.ID, ProjectID: chat.ProjectID}
	byID := make(map[string]int)
	for _, grant := range s.approvalGrants(chat.ID) {
		// Runtime chat capabilities bind directly to the root chat, with no project
		// field in their storage. Generic leases retain the shared scope predicate.
		runtimeChat := grant.Scope == hitl.ApprovalGrantScopeChat && grant.ChatSessionID == chat.ID && grant.ProjectID == "" && (grant.Predicate.Category == hitl.ApprovalGrantCategoryDirectIP || grant.Predicate.Category == hitl.ApprovalGrantCategorySocketPath || grant.Predicate.Category == hitl.ApprovalGrantCategorySocketCapability)
		if !runtimeChat && !settings.GrantScopeApplies(grant, action) {
			continue
		}
		if grant.ExpiresAt != nil && !grant.ExpiresAt.After(now) {
			continue
		}
		effects := hitl.ElevatedGrantEffects(grant)
		if grant.ID == "" || len(effects) == 0 {
			continue
		}
		if index, exists := byID[grant.ID]; exists {
			row := &summary.Records[index]
			row.Effects = append(row.Effects, effects...)
			slices.Sort(row.Effects)
			row.Effects = slices.Compact(row.Effects)
			continue
		}
		byID[grant.ID] = len(summary.Records)
		summary.Records = append(summary.Records, wire.ElevatedAccessRecord{
			ID: grant.ID, Kind: "grant", Title: grant.Title, Scope: wire.ApprovalGrantScope(grant.Scope), Effects: effects, ExpiresAt: grant.ExpiresAt,
		})
	}
	for _, quiet := range s.Gate.ListAskQuiets(chat.ID) {
		if len(quiet.ElevatedEffects) == 0 || (quiet.ExpiresAt != nil && !quiet.ExpiresAt.After(now)) {
			continue
		}
		summary.Records = append(summary.Records, wire.ElevatedAccessRecord{
			ID: quiet.ID, Kind: "quiet", Title: quiet.Label, Scope: wire.ApprovalGrantScopeChat,
			Effects: slices.Clone(quiet.ElevatedEffects), ExpiresAt: quiet.ExpiresAt,
		})
	}
	slices.SortFunc(summary.Records, func(a, b wire.ElevatedAccessRecord) int { return strings.Compare(a.ID, b.ID) })
	for _, row := range summary.Records {
		if row.Scope != wire.ApprovalGrantScopeChat && !slices.Contains(summary.SharedScopes, row.Scope) {
			summary.SharedScopes = append(summary.SharedScopes, row.Scope)
		}
	}
	summary.Total = len(summary.Records)
	return summary, nil
}

func (s *Handler) HandleGetElevatedAccess(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.elevatedAccessChat(w, r)
	if !ok {
		return
	}
	summary, err := s.elevatedAccessSummary(r.Context(), chat)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, summary)
}

func (s *Handler) HandleRevokeElevatedAccess(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.elevatedAccessChat(w, r)
	if !ok {
		return
	}
	release := s.lockApprovalRevocation()
	defer release()
	selected, err := s.elevatedAccessSummary(r.Context(), chat)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	results := make([]wire.ElevatedAccessRevokeResult, 0, selected.Total)
	for _, row := range selected.Records {
		results = append(results, s.revokeApprovalRecord(r.Context(), row.ID))
	}
	if len(results) > 0 {
		projectview.PublishSettings(s.Events, s.Projects, r.Context(), "approvals", string(llm.SettingsScopeGlobal), "", "updated")
	}
	remaining, err := s.elevatedAccessSummary(r.Context(), chat)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.RevokeElevatedAccessResponse{Results: results, Remaining: remaining})
}

func (s *Handler) elevatedApprovalsEnabled(ctx context.Context, chat *wire.Session) (bool, error) {
	ref := settings.ProjectRef{ID: chat.ProjectID}
	if chat.ProjectID != "" {
		p, err := s.Projects.Get(ctx, chat.ProjectID)
		if err != nil {
			return false, err
		}
		scope, err := requestscope.ResolveSessionProject(s.Store, ctx, p, chat.ID)
		if err != nil {
			return false, err
		}
		overlay, err := project.ResolveOverlay(scope.Roots, chat.WorkspaceRootID)
		if err != nil {
			return false, err
		}
		if err := overlay.CheckCompatibility(); err != nil {
			return false, err
		}
		ref.Dir = overlay.Active.Path
	}
	cfg := s.Settings.Approvals.Get(llm.SettingsScopeProject, ref)
	return cfg.NeverAsk == nil || !*cfg.NeverAsk, nil
}
