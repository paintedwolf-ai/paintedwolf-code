package settingsadmin

import (
	"context"
	"fmt"
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/projectview"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleGetApprovals(w http.ResponseWriter, r *http.Request) {
	scope, ref, err := requestscope.Settings(r, s.Projects)
	if err != nil {
		requestscope.ScopeError(s.responses, w, r, err)
		return
	}
	cfg := s.Service.Approvals.Get(scope, ref)
	httpio.WriteJSON(w, http.StatusOK, s.approvalConfigResponse(r.Context(), scope, ref, cfg))
}

func (s *Handler) approvalConfigResponse(ctx context.Context, scope llm.SettingsScope, ref settings.ProjectRef, cfg settings.ApprovalConfig) wire.ApprovalConfigResponse {
	projectDir := ref.Dir
	resp := settings.ApprovalConfigToDTO(
		settings.WireScope(string(scope)),
		cfg,
		s.Service.Approvals.MergedFrom(scope, projectDir),
	)
	catalog := s.Sessions.RuleLayers(ctx, ref.ID)
	managed := append([]settings.ApprovalRule(nil), catalog.Device...)
	if scope == llm.SettingsScopeProject {
		managed = append(managed, catalog.Project...)
	}
	for _, rule := range managed {
		resp.ManagedRules = append(resp.ManagedRules, wire.ManagedApprovalRule{
			Category: wire.ApprovalCategory(rule.Category), Pattern: rule.Pattern,
			Effect: wire.ApprovalEffect(rule.Effect), UnitID: rule.Source.UnitID,
			PackID: rule.Source.PackID, Scope: wire.ApprovalRuleScope(rule.Source.Scope),
		})
	}
	if scope == llm.SettingsScopeProject && projectDir != "" {
		overlay := s.Service.Approvals.ProjectOverlay(projectDir)
		global := s.Service.Approvals.Get(llm.SettingsScopeGlobal, settings.ProjectRef{})
		resp.FieldSources = &wire.ApprovalFieldSources{
			ApprovalPosture:    fieldSource(overlay.Posture != ""),
			AIRationaleEnabled: fieldSource(overlay.AIRationale != nil),
			NeverAsk:           fieldSource(overlay.NeverAsk != nil),
		}
		ai := true
		if global.AIRationale != nil {
			ai = *global.AIRationale
		}
		resp.Defaults = &wire.ApprovalDefaults{
			ApprovalPosture:    string(global.Posture),
			AIRationaleEnabled: ai,
			NeverAsk:           global.NeverAsk != nil && *global.NeverAsk,
		}
	}
	return resp
}

func fieldSource(overridden bool) string {
	if overridden {
		return "override"
	}
	return "default"
}

func (s *Handler) HandleUpdateApprovals(w http.ResponseWriter, r *http.Request) {
	scope, ref, err := requestscope.Settings(r, s.Projects)
	if err != nil {
		requestscope.ScopeError(s.responses, w, r, err)
		return
	}
	projectDir := ref.Dir
	var raw map[string]any
	var req wire.UpdateApprovalsSettingsRequest
	if err := httpio.DecodeJSONWithRaw(w, r, &req, &raw); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}

	var rules []settings.ApprovalRule
	rulesPresent := false
	if _, ok := raw["rules"]; ok {
		rulesPresent = true
		if raw["rules"] != nil {
			rules = settings.ApprovalRulesFromDTO(req.Rules)
			for i, rule := range rules {
				if err := settings.ValidatePolicyRules([]settings.ApprovalRule{rule}); err != nil {
					s.responses.Logger.DebugContext(r.Context(), "approval rule rejected", "index", i, "err", err)
					s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": fmt.Sprintf("rules[%d]", i)},
						"the approval rule has an unknown category or effect, or an invalid pattern")
					return
				}
			}
		}
	}

	switch scope {
	case llm.SettingsScopeProject:
		existing := s.Service.Approvals.ProjectOverlay(projectDir)
		if !rulesPresent {
			rules = existing.Rules
		}

		posture := existing.Posture
		if _, ok := raw["approval_posture"]; ok {
			if raw["approval_posture"] == nil {
				posture = ""
			} else {
				if req.ApprovalPosture == nil || !gate.ValidPosture(*req.ApprovalPosture) {
					s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "approval_posture"}, "unknown approval_posture (light|balanced|strict)")
					return
				}
				posture = gate.PostureFromString(*req.ApprovalPosture)
			}
		}
		if global := s.Service.Approvals.Posture(); gate.Stricter(global, posture) != posture && posture != "" {
			s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "approval_posture"},
				"project approval_posture may only be stricter than the global posture ("+string(global)+")")
			return
		}

		aiRationale := existing.AIRationale
		if _, ok := raw["ai_rationale_enabled"]; ok {
			if raw["ai_rationale_enabled"] == nil {
				aiRationale = nil
			} else if req.AIRationaleEnabled != nil {
				v := *req.AIRationaleEnabled
				aiRationale = &v
			}
		}

		neverAsk := existing.NeverAsk
		if _, ok := raw["never_ask"]; ok {
			if raw["never_ask"] == nil {
				neverAsk = nil
			} else if req.NeverAsk != nil && *req.NeverAsk {
				s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "never_ask"},
					"project never_ask may only be false to restore approvals")
				return
			} else if req.NeverAsk != nil {
				v := false
				neverAsk = &v
			}
		}

		cfg := settings.ApprovalConfig{
			Rules:       rules,
			Posture:     posture,
			AIRationale: aiRationale,
			NeverAsk:    neverAsk,
		}
		if err := s.Service.Approvals.PutProject(projectDir, cfg); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	default:
		existingRules := s.Service.Approvals.OverlayRules()
		if !rulesPresent {
			rules = existingRules
		}

		posture := s.Service.Approvals.Posture()
		if _, ok := raw["approval_posture"]; ok {
			if raw["approval_posture"] == nil {
				posture = gate.PostureBalanced
			} else {
				if req.ApprovalPosture == nil || !gate.ValidPosture(*req.ApprovalPosture) {
					s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "approval_posture"}, "unknown approval_posture (light|balanced|strict)")
					return
				}
				posture = gate.PostureFromString(*req.ApprovalPosture)
			}
		}

		aiRationale := s.Service.Approvals.AIRationaleEnabled()
		if _, ok := raw["ai_rationale_enabled"]; ok {
			if raw["ai_rationale_enabled"] == nil {
				aiRationale = true
			} else if req.AIRationaleEnabled != nil {
				aiRationale = *req.AIRationaleEnabled
			}
		}

		neverAsk := s.Service.Approvals.NeverAsk()
		if _, ok := raw["never_ask"]; ok {
			if raw["never_ask"] == nil {
				neverAsk = false
			} else if req.NeverAsk != nil {
				neverAsk = *req.NeverAsk
			}
		}

		cfg := settings.ApprovalConfig{
			Rules:       rules,
			Grants:      s.Service.Approvals.GlobalGrants(),
			Posture:     posture,
			AIRationale: &aiRationale,
			NeverAsk:    &neverAsk,
		}
		if err := s.Service.Approvals.PutGlobal(cfg); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		confine.SetEgressPosture(s.Service.Approvals.EgressPosture())
	}
	projectview.PublishSettings(s.Events, s.Projects, r.Context(), wire.SettingsAreaApprovals, string(scope), projectDir, "updated")
	httpio.WriteJSON(w, http.StatusOK, s.approvalConfigResponse(r.Context(), scope, ref, s.Service.Approvals.Get(scope, ref)))
}
