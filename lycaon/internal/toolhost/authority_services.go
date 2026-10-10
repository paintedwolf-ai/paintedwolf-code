package toolhost

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
)

// AuthorityServices owns host approval and confinement wiring.
type AuthorityServices struct {
	approvalGate        hitl.ApprovalGate
	approvalRules       settings.ApprovalRuleCatalogSource
	authzRecorder       authzledger.Recorder
	gateBuilder         *settings.GateBuilder
	gateHandle          *settings.DeferredGate
	approvals           *settings.ApprovalStore
	rejectFmt           *guidance.StaticRejectFormatter
	profilePolicy       *toolprofiles.ProfilePolicyEngine
	reviews             *toolexecution.Approvals
	paths               *toolexecution.Boundary
	capabilities        *toolexecution.Capabilities
	metadata            *toolexecution.Metadata
	rejections          *toolexecution.Rejections
	releaseEgressPolicy func()
}

func (r *AuthorityServices) SetApprovalRuleSource(src settings.ApprovalRuleCatalogSource) {
	if r == nil || src == nil {
		return
	}
	r.approvalRules = src
	if r.gateBuilder != nil {
		r.gateBuilder.WithApprovalRules(src)
	}
}

func (r *AuthorityServices) SealApprovalGate() {
	if r == nil || r.gateBuilder == nil {
		return
	}
	r.gateBuilder.Seal()
}

func (r *AuthorityServices) ApprovalGateSealed() bool {
	return r != nil && (r.gateBuilder == nil || r.gateHandle.Sealed())
}

func (r *AuthorityServices) SetEgressDetectionSource(src confine.EgressDetectionSource) {
	if r == nil {
		return
	}
	confine.SetEgressDetectionSource(src)
	// Wired with or without a store: a nil reader hands the seam an empty posture.
	store := r.approvals
	confine.SetDetectionApprovalPosture(func(cmd confine.EgressCommand) gate.Posture {
		return effectiveEgressApprovalConfig(store, cmd).Posture
	})
}

func (r *AuthorityServices) ApprovalPosture(projectDir string) gate.Posture {
	if r == nil || r.approvals == nil {
		return gate.DefaultPosture
	}
	return effectiveEgressApprovalConfig(r.approvals, confine.EgressCommand{ProjectDir: projectDir}).Posture
}

func (r *AuthorityServices) ApprovalsDisabled(projectDir string) bool {
	if r == nil || r.approvals == nil {
		return false
	}
	cfg := effectiveEgressApprovalConfig(r.approvals, confine.EgressCommand{ProjectDir: projectDir})
	return cfg.NeverAsk != nil && *cfg.NeverAsk
}

func (r *AuthorityServices) WriteRootRule(ctx context.Context, projectID, projectDir, root string) (settings.ApprovalRule, bool) {
	if r == nil || r.approvals == nil {
		return settings.ApprovalRule{}, false
	}
	scope := llm.SettingsScopeGlobal
	ref := settings.ProjectRef{ID: projectID, Dir: projectDir}
	if projectID != "" || projectDir != "" {
		scope = llm.SettingsScopeProject
	}
	layers := settings.EffectiveApprovalRuleLayers(ctx, r.approvals, r.approvalRules, scope, ref)
	return settings.EvaluateWriteRootRuleLayers(layers, root)
}

func (r *AuthorityServices) SetMCPToolPinSource(src settings.MCPToolPinSource) {
	if r == nil || r.gateBuilder == nil {
		return
	}
	r.gateBuilder.WithPins(src)
}

func (r *AuthorityServices) SetAuthzRecorder(rec authzledger.Recorder) {
	if r == nil || r.reviews == nil {
		return
	}
	r.authzRecorder = rec
	r.reviews.SetAuthzRecorder(rec)
}

func (r *AuthorityServices) ReleaseSessionRun(sessionID string) {
	if r == nil {
		return
	}
	confine.ForgetEgressSession(sessionID)
}

func (r *AuthorityServices) ForgetSessionAuthorization(sessionID string) {
	if r == nil {
		return
	}
	if r.approvalGate != nil {
		r.approvalGate.ForgetSession(sessionID)
	}
}

func (r *AuthorityServices) SetCheckpointManager(mgr hitl.CheckpointManager) {
	if r == nil {
		return
	}
	if r.reviews != nil {
		r.reviews.SetCheckpointManager(mgr, r.approvalGate)
	}
}

func (r *AuthorityServices) ApprovalGate() hitl.ApprovalGate {
	if r == nil {
		return nil
	}
	return r.approvalGate
}

func (r *AuthorityServices) SetSandboxListenGate(listen tools.LocalListenGate) {
	if r == nil {
		return
	}
	if r.paths != nil {
		r.paths.SetSessionListenGrant(listen.SessionListenGrant)
		r.capabilities.SetLocalListenGate(listen)
	}
}

func (r *AuthorityServices) SetSandboxLoopbackGate(loopback tools.LoopbackConnectGate) {
	if r == nil {
		return
	}
	if r.paths != nil {
		r.paths.SetSessionLoopbackGrant(loopback.SessionLoopbackGrant)
		r.capabilities.SetLoopbackConnectGate(loopback)
	}
}

func (r *AuthorityServices) SetLocalNetworkGate(localNetwork tools.LocalNetworkGate) {
	if r == nil || r.paths == nil {
		return
	}
	r.capabilities.SetLocalNetworkGate(localNetwork)
}

func (r *AuthorityServices) SetToolApprovalCoalesce(rt *approvalstate.ToolApprovalCoalesce) {
	if r == nil || r.reviews == nil || rt == nil {
		return
	}
	r.reviews.SetToolApprovalCoalesce(toolApprovalCoalesceAdapter{rt: rt})
}

func (r *AuthorityServices) SetGateRepeatLedger(rt *approvalstate.GateRepeatLedger) {
	if r == nil || r.reviews == nil || rt == nil {
		return
	}
	r.reviews.SetGateRepeatLedger(gateRepeatLedgerAdapter{rt: rt})
}

func (r *AuthorityServices) ApplyGuidanceRejects(formatter *guidance.StaticRejectFormatter) {
	if r == nil || r.metadata == nil || formatter == nil {
		return
	}
	r.rejectFmt = formatter
	approvalPolicy := toolexecution.NewApprovalPolicyEngine(r.profilePolicy, r.approvalGate)
	r.metadata.SetPolicy(toolprofiles.NewGuidanceRejectPolicy(approvalPolicy))
	r.rejections.SetRejectFormatter(formatter)
}

func effectiveEgressApprovalRules(ctx context.Context, runtime *AuthorityServices, store *settings.ApprovalStore, cmd confine.EgressCommand) settings.ApprovalRuleLayers {
	scope := llm.SettingsScopeGlobal
	ref := settings.ProjectRef{}
	if cmd.ProjectID != "" || cmd.ProjectDir != "" {
		scope = llm.SettingsScopeProject
		ref = settings.ProjectRef{ID: cmd.ProjectID, Dir: cmd.ProjectDir}
	}
	var source settings.ApprovalRuleCatalogSource
	if runtime != nil {
		source = runtime.approvalRules
	}
	return settings.EffectiveApprovalRuleLayers(ctx, store, source, scope, ref)
}

func recordEgressRuleDeny(
	ctx context.Context,
	runtime *AuthorityServices,
	cmd confine.EgressCommand,
	host string,
	rule settings.ApprovalRule,
) {
	if runtime == nil || runtime.authzRecorder == nil {
		return
	}
	tool := strings.TrimSpace(cmd.Image)
	if tool == "" {
		tool = "command"
	}
	runtime.authzRecorder.AppendToolDenied(ctx, authzledger.ToolDeniedRecord{
		SessionID: cmd.SessionID, ParentSessionID: cmd.RootSessionID,
		Tool: tool, Args: map[string]any{"command": cmd.CommandLine, "host": host},
		ProjectDir: cmd.ProjectDir, RejectCode: "APPROVAL_RULE_DENIED",
		BlockReason: "APPROVAL_RULE_DENIED",
		ApprovalRules: []authzledger.ApprovalRuleCitation{{
			Category: string(rule.Category), Pattern: rule.Pattern, Effect: string(rule.Effect),
			UnitID: rule.Source.UnitID, PackID: rule.Source.PackID, Scope: string(rule.Source.Scope),
		}},
	})
}

func wireEgressPolicy(runtime *AuthorityServices, store *settings.ApprovalStore) {
	runtime.releaseEgressPolicy = confine.SetEgressRuleEvaluator(func(ctx context.Context, cmd confine.EgressCommand, host string) confine.EgressRuleResult {
		layers := effectiveEgressApprovalRules(ctx, runtime, store, cmd)
		rule, ok := settings.EvaluateHostRuleLayers(layers, host)
		if !ok {
			return confine.EgressRuleResult{}
		}
		result := confine.EgressRuleResult{
			Pattern: rule.Pattern, UnitID: rule.Source.UnitID,
			PackID: rule.Source.PackID, Scope: string(rule.Source.Scope),
		}
		switch rule.Effect {
		case settings.ApprovalEffectDeny:
			result.Effect = confine.EgressRuleDeny
			recordEgressRuleDeny(ctx, runtime, cmd, host, rule)
		case settings.ApprovalEffectAsk:
			result.Effect = confine.EgressRuleAsk
		}
		return result
	})
	confine.SetEgressPosture(store.EgressPosture())
	confine.SetEgressPostureResolver(func(cmd confine.EgressCommand) confine.EgressPosture {
		return settings.EgressPostureFor(effectiveEgressApprovalConfig(store, cmd).Posture)
	})
	confine.SetApprovalsDisabledSource(func(cmd confine.EgressCommand) bool {
		cfg := effectiveEgressApprovalConfig(store, cmd)
		return cfg.NeverAsk != nil && *cfg.NeverAsk
	})
}

func effectiveEgressApprovalConfig(store *settings.ApprovalStore, cmd confine.EgressCommand) settings.ApprovalConfig {
	if store == nil {
		return settings.ApprovalConfig{Posture: gate.DefaultPosture}
	}
	if projectDir := filepath.Clean(cmd.ProjectDir); cmd.ProjectDir != "" && projectDir != "." {
		return store.Get(llm.SettingsScopeProject, settings.ProjectRef{ID: cmd.ProjectID, Dir: projectDir})
	}
	return store.Get(llm.SettingsScopeGlobal, settings.ProjectRef{})
}

// ReleaseEgressPolicy detaches this host's process policy callback.
func (r *AuthorityServices) ReleaseEgressPolicy() {
	if r != nil && r.releaseEgressPolicy != nil {
		r.releaseEgressPolicy()
		r.releaseEgressPolicy = nil
	}
}
