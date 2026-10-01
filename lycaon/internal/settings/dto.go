package settings

import (
	"strings"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// ApprovalConfigToDTO converts internal config to API response.
func ApprovalConfigToDTO(scope wire.SettingsScope, cfg ApprovalConfig, mergedFrom []string) wire.ApprovalConfigResponse {
	rules := make([]wire.ApprovalRule, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		rules = append(rules, wire.ApprovalRule{
			Category: wire.ApprovalCategory(r.Category),
			Pattern:  r.Pattern,
			Effect:   wire.ApprovalEffect(r.Effect),
		})
	}
	aiRationaleEnabled := true
	if cfg.AIRationale != nil {
		aiRationaleEnabled = *cfg.AIRationale
	}
	neverAsk := false
	if cfg.NeverAsk != nil {
		neverAsk = *cfg.NeverAsk
	}
	return wire.ApprovalConfigResponse{
		Scope:              scope,
		Rules:              rules,
		ManagedRules:       []wire.ManagedApprovalRule{},
		NeverAsk:           neverAsk,
		MergedFrom:         mergedFrom,
		ApprovalPosture:    string(cfg.Posture),
		AIRationaleEnabled: aiRationaleEnabled,
	}
}

// ApprovalRulesFromDTO converts wire rules to internal rules (no granted_at).
func ApprovalRulesFromDTO(rules []wire.ApprovalRule) []ApprovalRule {
	out := make([]ApprovalRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, ApprovalRule{
			Category: ApprovalCategory(r.Category),
			Pattern:  r.Pattern,
			Effect:   ApprovalEffect(r.Effect),
		})
	}
	return out
}

// LimitsToDTO converts internal limits to API response.
func LimitsToDTO(scope wire.SettingsScope, lim SessionLimits, mergedFrom []string) wire.SettingsLimitsResponse {
	nanoUSD, _ := cost.USDToNano(lim.SessionSpendCeilingUSD)
	return wire.SettingsLimitsResponse{
		Scope:                        scope,
		MaxIterations:                lim.MaxIterations,
		OverlayPromoteMaxIterations:  lim.OverlayPromoteMaxIterations,
		MaxToolResultBytes:           lim.MaxToolResultBytes,
		SessionSpendCeilingNanoUSD:   nanoUSD,
		SpendWarningRatio:            lim.SpendWarningRatio,
		SpendCeilingEnabled:          lim.SpendCeilingEnabled,
		SpendSoftStop:                lim.SpendSoftStopEnabled(),
		CoordinatorLoop:              lim.CoordinatorLoop,
		MaxCoordinatorLoopCycles:     lim.MaxCoordinatorLoopCycles,
		LLMTurnTimeoutMs:             lim.LLMTurnTimeoutSec * 1000,
		CoordinatorHostTurnTimeoutMs: lim.CoordinatorHostTurnTimeoutSec * 1000,
		CoordinatorMaxSleepMs:        lim.CoordinatorMaxSleepSec * 1000,
		AwaitParentWorkersTimeoutMs:  lim.AwaitParentWorkersTimeoutSec * 1000,
		WorkerToolBudgetDefault:      lim.WorkerToolBudgetDefault,
		WorkerToolBudgetMin:          lim.WorkerToolBudgetMin,
		WorkerToolBudgetMax:          lim.WorkerToolBudgetMax,
		MergedFrom:                   mergedFrom,
	}
}

// WireScope converts llm scope to API scope.
func WireScope(scope string) wire.SettingsScope {
	return wire.SettingsScope(scope)
}

// SecurityScannersToDTO converts effective Security scanners settings to API response.
func SecurityScannersToDTO(cfg SecurityScannersConfig, mergedFrom []string) wire.SecurityScannersSettingsResponse {
	scope := wire.LandedChangeScope(cfg.LandedChangeScope)
	if !wire.IsKnownLandedChangeScope(scope) {
		scope = wire.LandedChangeScopePathScoped
	}
	verify := wire.SourceVerify(cfg.SourceVerify)
	if !wire.IsKnownSourceVerify(verify) {
		verify = wire.SourceVerifyStat
	}
	return wire.SecurityScannersSettingsResponse{
		Enabled:           cfg.Enabled,
		MergedFrom:        mergedFrom,
		LandedChangeScope: scope,
		SourceVerify:      verify,
	}
}

// ReviewToDTO converts internal review config to API response.
func ReviewToDTO(scope wire.SettingsScope, cfg ReviewConfig, mergedFrom []string) wire.ReviewSettingsResponse {
	rules := make([]wire.ContentReviewRule, 0, len(cfg.ReviewPaths))
	for _, r := range cfg.ReviewPaths {
		rules = append(rules, wire.ContentReviewRule{
			Tool:            r.Tool,
			Path:            r.Path,
			Reason:          r.Reason,
			DisabledUntilAt: r.DisabledUntil,
		})
	}
	return wire.ReviewSettingsResponse{
		Scope:       scope,
		ReviewPaths: rules,
		MergedFrom:  mergedFrom,
	}
}

// ReviewFromDTO converts API request to internal review config.
func ReviewFromDTO(req wire.UpdateReviewSettingsRequest) ReviewConfig {
	rules := make([]ContentReviewRule, 0, len(req.ReviewPaths))
	for _, r := range req.ReviewPaths {
		rules = append(rules, ContentReviewRule{
			Tool:          r.Tool,
			Path:          r.Path,
			Reason:        r.Reason,
			DisabledUntil: r.DisabledUntilAt,
		})
	}
	return ReviewConfig{ReviewPaths: rules}
}

// VerifyToDTO converts internal verify config to API response.
func VerifyToDTO(scope wire.SettingsScope, projectDir string, cfg VerifyConfig, detect VerifyDetectCandidate, hasDetect bool) wire.VerifySettingsResponse {
	resp := wire.VerifySettingsResponse{
		Scope:             scope,
		Test:              cfg.Test,
		VerifyPath:        settingsoverlay.Rel("verify.yaml"),
		BackendConfigured: true,
	}
	// Suggestion semantics are per-project; global scope carries no nudge state.
	if scope == wire.SettingsScopeProject && strings.TrimSpace(projectDir) != "" {
		declared := strings.TrimSpace(cfg.Test) != ""
		hasCommand := hasDetect && strings.TrimSpace(detect.Command) != ""
		// Surface the detected command (for the nudge, and so Settings can show a
		// dismissed suggestion) whenever one exists and none is declared.
		if !declared && hasCommand {
			resp.DetectedCommand = detect.Command
			resp.DetectedSource = detect.Source
		}
		resp.Dismissed = detect.Dismissed
		switch {
		case declared:
			resp.SuggestionState = wire.VerifySuggestionStateAccepted
		case detect.Dismissed:
			resp.SuggestionState = wire.VerifySuggestionStateDismissed
		case hasCommand:
			resp.SuggestionState = wire.VerifySuggestionStateSuggest
		case hasDetect:
			resp.SuggestionState = wire.VerifySuggestionStateWaiting
		default:
			resp.SuggestionState = wire.VerifySuggestionStateUnknown
		}
	}
	return resp
}

// VerifyFromDTO converts API request to internal verify config.
func VerifyFromDTO(req wire.UpdateVerifySettingsRequest) VerifyConfig {
	return normalizeVerifyConfig(VerifyConfig{Test: req.Test})
}
