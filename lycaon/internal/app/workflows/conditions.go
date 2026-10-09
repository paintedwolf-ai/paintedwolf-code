package workflows

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/session"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/vocabulary"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowblueprintfiles "github.com/lycaon/lycaon/internal/workflow/blueprintfiles"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	"github.com/lycaon/lycaon/internal/workflowdiag"
)

// ConditionDependencies declares dependencies needed to construct condition and rule engines.
type ConditionDependencies struct {
	ModuleRoot              string
	DelegationStore         *delegation.SQLStore
	ScanStore               *scan.SQLStore
	ScanObligation          *scan.WorkflowObligation
	Snapshots               *sourcesnapshot.Store
	GatesCfg                scancfg.GatesConfig
	SecurityScannersEnabled func() bool
	Checkpoints             hitl.CheckpointManager
	WorkerQueue             *worker.SQLQueue
	Agents                  *orchestration.MemoryAgentRegistry
	Postures                *session.PostureRegistry
	EffectiveCatalog        *extpacks.EffectiveCatalog
	TestTemplatesDir        string
	ProjectSettingsGate     *settings.ProjectSurfaceGate
	SourceVerifyPassed      func(ctx context.Context, sessionID string) (bool, error)
	DeliveryReported        func(ctx context.Context, sessionID, runID, phase string) (bool, error)
}

// BuildConditions initializes condition evaluation, rules engine, and composition services.
func (r *Runtime) BuildConditions(ctx context.Context, deps ConditionDependencies) error {
	proactiveCategories := deps.GatesCfg.Gates.ProactiveCategories
	if len(proactiveCategories) == 0 {
		proactiveCategories = scancfg.DefaultGatesConfig().Gates.ProactiveCategories
	}

	condReg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		DelegationStore: deps.DelegationStore,
		Evidence:        conditions.StoreEvidenceReader{Store: r.Evidence},
		ObligationResolvers: map[string]conditions.ObligationStatusReader{
			scan.WorkflowObligationKind: deps.ScanObligation,
		},
		BlueprintContent: func(_ context.Context, projectDir, relPath string) (string, error) {
			return workflowblueprintfiles.ReadBlueprintFile(projectDir, relPath)
		},
		DelegationCloseout:      delegation.CloseoutComplete(deps.DelegationStore),
		SourceVerifyPassed:      deps.SourceVerifyPassed,
		DeliveryReported:        deps.DeliveryReported,
		ScanLedger:              deps.ScanStore,
		SourceSnapshots:         deps.Snapshots,
		ScanProactiveCategories: proactiveCategories,
		SecurityScannersEnabled: deps.SecurityScannersEnabled,
		ApprovalDenied:          deps.Checkpoints.SessionApprovalDenied,
		WorkerCycleIdle: func(projectID, sessionID, completingJobID string) (bool, error) {
			return session.ParentSessionWorkerCycleIdle(ctx, deps.WorkerQueue, projectID, sessionID, completingJobID)
		},
		ChildRunStatus: func(parentRunID string) (string, bool) {
			child, cErr := r.Store.Runs.LatestChildByParentRunID(ctx, parentRunID)
			if cErr != nil || child == nil {
				return "", false
			}
			return string(child.Status), true
		},
	})
	if err != nil {
		return fmt.Errorf("condition registry: %w", err)
	}
	r.Conditions = condReg
	if err := rules.RegisterRuleConditions(r.Conditions); err != nil {
		return fmt.Errorf("rule conditions: %w", err)
	}

	bundledRules, err := rules.LoadBundledRules()
	if err != nil {
		return fmt.Errorf("rules config: %w", err)
	}
	r.BundledRules = bundledRules

	if err := rules.ValidatePostureRules(deps.Postures, sessionposture.AllSessionPostures(), r.BundledRules); err != nil {
		return fmt.Errorf("posture rules: %w", err)
	}

	ruleConfigs := make([]*rules.RulesConfig, 0, len(r.BundledRules))
	for _, cfg := range r.BundledRules {
		ruleConfigs = append(ruleConfigs, cfg)
	}
	if diags := vocabulary.ValidateBundled(r.Conditions, r.Manifests, ruleConfigs); len(diags) > 0 {
		return fmt.Errorf("vocabulary validation: %s", workflowdiag.Summarize(diags))
	}
	r.Manager.SetConditionRegistry(r.Conditions)

	obligationSpecs := workflow.ObligationSpecsFromKinds(r.Manager.Obligations.Kinds)
	composer := &workflowcomposition.Composer{
		ModuleRoot:   deps.ModuleRoot,
		SessionStore: r.Drafts,
		Registry:     r.Conditions,
		Obligations:  obligationSpecs,
		Agents:       deps.Agents,
	}
	if composePolicy, err := workflowcomposition.LoadComposePolicy(); err != nil {
		return fmt.Errorf("compose policy: %w", err)
	} else {
		composer.Policy = composePolicy
	}

	loadTemplates := func() (workflowcomposition.TemplateCatalog, error) {
		if deps.TestTemplatesDir != "" {
			return workflowcomposition.LoadTemplatesFromDir(extpacks.OnDisk(deps.TestTemplatesDir))
		}
		return workflowcomposition.LoadTemplatesEffective(deps.EffectiveCatalog)
	}
	templates, err := loadTemplates()
	if err != nil {
		return fmt.Errorf("workflow templates: %w", err)
	}
	composer.Templates = templates
	r.Composer = composer

	r.Persister = &workflowcomposition.Persister{
		ModuleRoot:   deps.ModuleRoot,
		SessionStore: r.Drafts,
		Registry:     r.Conditions,
		Obligations:  obligationSpecs,
		Agents:       deps.Agents,
		Policy:       composer.Policy,
	}

	ruleEngine, err := rules.NewPostureRuleEngine(deps.Postures, r.BundledRules, r.Conditions)
	if err != nil {
		return fmt.Errorf("posture rule engine: %w", err)
	}
	r.Rules = ruleEngine
	r.ProjectRules = rules.NewProjectRulesOverlay(r.Conditions)
	r.Rules.Overlay = r.ProjectRules
	if deps.ProjectSettingsGate != nil {
		r.Rules.ProjectSettingsApply = deps.ProjectSettingsGate.Applies
	}
	return nil
}
