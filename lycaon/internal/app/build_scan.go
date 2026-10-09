package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/scan"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanexecution "github.com/lycaon/lycaon/internal/scan/execution"
	scanregistry "github.com/lycaon/lycaon/internal/scan/registry"
	scantoolapi "github.com/lycaon/lycaon/internal/scan/toolapi"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/workflow"
	"github.com/lycaon/lycaon/pkg/api"
)

func (b toolWiring) wireScan() error {
	if _, err := sessionWiring(b).loadSecretMatcher(); err != nil {
		return err
	}
	if err := b.wireSourceScope(); err != nil {
		return err
	}
	runnerCfg := scancfg.DefaultRunnerConfig()
	if b.cfg.TestScanRegistry != nil {
		b.scannerReg = b.cfg.TestScanRegistry
	} else {
		reg, err := scanregistry.New(scanregistry.Options{
			ScannerFingerprintKey: b.secretFingerprinter.ScannerKey(),
			ModuleRoot:            b.configRoot,
			ProcessPriority:       runnerCfg.ExecProcessPriority(),
			ProjectTierApplies:    b.projectScanConfigGate().AppliesPath,
		})
		if err != nil {
			return fmt.Errorf("scan registry: %w", err)
		}
		b.scannerReg = reg
		// Library scanners keep a worker process per adapter.
		b.resources.track("scanner-workers", 52, func(context.Context) error { return reg.Close() })
	}
	b.scanCoordinator.Registry = b.scannerReg
	b.scanStore.SecretIgnores = b.scanSecretIgnores
	scanIngester := &scan.IngesterImpl{
		SecretIgnores:           b.scanSecretIgnores,
		Inspector:               b.simpleInspector,
		Module:                  scancfg.DefaultModuleConfig(),
		Budget:                  scancfg.NewFindingBudget(b.gatesCfg.Gates.AgentBudget),
		BlockOn:                 b.gatesCfg.Gates.BlockOn,
		OverlayRootsApply:       b.projectScanConfigGate().FilterPaths,
		RecordWithoutDelegation: true,
	}
	b.scanRunner = scanexecution.NewRunner(b.scanStore, b.scannerReg, scanIngester, runnerCfg, b.eventPub)
	b.scanRunner.DataDir = b.dataDir
	if b.sourceLedger != nil {
		b.scanRunner.Snapshots = b.sourceLedger.SnapshotStore()
	}
	b.scanRunner.Coordinator = b.scanCoordinator
	b.scanRunner.Settings = b.settingsSvc.SecurityScanners
	b.scanRunner.OnDelta = func(ctx context.Context, completed api.CodeScan, introduced, fixed []api.SecurityFinding) {
		if completed.TargetKind == api.ScanTargetPaths {
			b.mgr.NoteScanDelta(ctx, completed, introduced, fixed)
		}
	}
	b.scanRunner.OnTerminal = func(ctx context.Context, completed api.CodeScan) {
		if b.scanCadence != nil {
			b.scanCadence.OnTerminal(ctx, completed)
		}
		b.mgr.NudgeCoordinatorScanDone(ctx, completed)
		if err := b.bindMovedFileRescans(ctx, completed); err != nil {
			slog.WarnContext(ctx, "bind moved-file rescan", "scan_id", completed.ID, "error", err)
		}
		if err := b.reconcileScanWorkflowTerminals(ctx, completed.ID); err != nil {
			slog.ErrorContext(ctx, "settle scan workflow consumers", "scan_id", completed.ID, "error", err)
		}
		if completed.DelegationID != "" && b.delegationMgr != nil {
			_ = b.delegationMgr.RetryCloseout(ctx, completed.DelegationID)
		}
	}
	b.scanRunner.ReconcileTerminal = func(ctx context.Context) error {
		return b.reconcileScanWorkflowTerminals(ctx, "")
	}
	b.mgr.SetScanWaitState(session.ScanWaitState{
		InFlight: func(ctx context.Context, sessionID string) bool {
			requested, err := b.scanStore.ListBySessionID(ctx, sessionID)
			if err != nil {
				// Read failures keep the timer backstop armed.
				return true
			}
			for _, requestedScan := range requested {
				if requestedScan.Status == api.CodeScanStatusPending || requestedScan.Status == api.CodeScanStatusRunning {
					return true
				}
			}
			// A pass waiting for a busy scanner has no scan yet.
			owed, err := b.scanStore.FullPassOwedForSession(ctx, sessionID)
			return err != nil || owed
		},
		Requested: func(ctx context.Context, sessionID, scanID string) bool {
			requested, err := b.scanStore.ListBySessionID(ctx, sessionID)
			if err != nil {
				return false
			}
			for _, requestedScan := range requested {
				if requestedScan.ID == scanID {
					return true
				}
			}
			return false
		},
	})
	b.scanTriggers = &scan.TriggerService{
		Coordinator: b.scanCoordinator,
		Registry:    b.scannerReg,
		Gates:       b.gatesCfg,
		Settings:    b.settingsSvc.SecurityScanners,
	}
	b.scanCadence = scancadence.New(b.scanStore, b.scanCoordinator, b.scannerReg, b.settingsSvc.SecurityScanners, b.gatesCfg, b.scanTriggers)
	b.scanCadence.OverlayRootsApply = b.projectScanConfigGate().FilterPaths
	b.scanCadence.Preempt = b.scanRunner.Preempt
	b.scanCadence.Scopes = b.sourceScopes
	b.resources.releaseObserver("scan-cadence-repochange", b.scanCadence.ObserveRepochange())
	if b.workerMergeSvc != nil {
		b.workerMergeSvc.Scans = b.scanCadence
	}
	b.scanObligation.Triggers = b.scanTriggers
	b.scanObligation.Full = b.scanCadence

	scanProvider := scan.NewGuidanceProvider(b.scanCoordinator, b.gatesCfg.Gates.AgentBudget)
	scanProvider.SetInjectRenderer(b.injectRenderer)
	b.scanGuidance = &scan.SessionGuidanceAdapter{
		Provider: scanProvider,
		DelegationBySession: func(sessionID string) (string, string, bool) {
			delegationID, ok := b.delegationStore.DelegationBySessionID(sessionID)
			if !ok {
				return "", "", false
			}
			return delegationID, "", true
		},
	}
	b.mgr.SetScanGuidance(b.scanGuidance)
	if err := scantoolapi.RegisterScanTools(b.toolRuntime.Registry, b.scanCoordinator, b.scannerReg, b.scanCadence, b.rejectFmt, b.settingsSvc.SecurityScanners); err != nil {
		return fmt.Errorf("scan tools: %w", err)
	}
	if err := workflow.RegisterComposeTool(b.toolRuntime.Registry, b.workflowComposer); err != nil {
		return fmt.Errorf("workflow_compose tool: %w", err)
	}
	if err := workflow.RegisterComposeFromTemplateTool(b.toolRuntime.Registry, b.workflowComposer); err != nil {
		return fmt.Errorf("workflow_compose_from_template tool: %w", err)
	}
	catalogResolver := workflow.ManifestResolver{
		SessionStore:       b.sessionWorkflowStore,
		ProjectTierApplies: b.projectScanConfigGate().AppliesPath,
	}
	if err := workflow.RegisterCatalogSummariesTool(b.toolRuntime.Registry, catalogResolver, b.sessionWorkflowStore, b.workflowComposer.Templates); err != nil {
		return fmt.Errorf("workflow_catalog_summaries tool: %w", err)
	}
	if err := workflow.RegisterPersistTool(b.toolRuntime.Registry, b.workflowPersister); err != nil {
		return fmt.Errorf("workflow_persist tool: %w", err)
	}
	if err := workflow.RegisterFeedbackTool(b.toolRuntime.Registry, b.workflowMgr); err != nil {
		return fmt.Errorf("workflow_user_feedback tool: %w", err)
	}
	if err := workflow.RegisterAskUserTool(b.toolRuntime.Registry, b.workflowMgr, b.toolRuntime.Boundary); err != nil {
		return fmt.Errorf("ask_user tool: %w", err)
	}
	if err := workflow.RegisterAdvanceTool(b.toolRuntime.Registry, b.workflowMgr); err != nil {
		return fmt.Errorf("workflow_advance tool: %w", err)
	}
	if err := workflow.RegisterTransitionTool(b.toolRuntime.Registry, b.workflowMgr); err != nil {
		return fmt.Errorf("workflow_transition tool: %w", err)
	}
	if err := workflow.RegisterFanoutPlanTool(b.toolRuntime.Registry, b.workflowMgr); err != nil {
		return fmt.Errorf("fanout_plan tool: %w", err)
	}
	if err := workflow.RegisterSubmitVerdictTool(b.toolRuntime.Registry, b.workflowMgr); err != nil {
		return fmt.Errorf("submit_verdict tool: %w", err)
	}
	return nil
}

// wireSourceScope builds the one scope provider every host-initiated reader
// of a project tree shares: the bundled scope, the device overlay, and each
// trusted project's declarations.
func (b toolWiring) wireSourceScope() error {
	cfg, err := sourcescope.LoadConfig(filepath.Join(b.dataDir, sourceScopeOverlayName))
	if err != nil {
		return fmt.Errorf("source scope: %w", err)
	}
	provider, err := sourcescope.NewProvider(cfg, b.projectScanConfigGate().AppliesPath)
	if err != nil {
		return fmt.Errorf("source scope: %w", err)
	}
	b.sourceScopes = provider
	if b.sourceLedger != nil && b.sourceLedger.SnapshotStore() != nil {
		b.sourceLedger.SnapshotStore().SetScopes(provider)
	}
	sourcecatalog.Process().SetScopes(provider)
	return nil
}

// sourceScopeOverlayName is the device overlay under the engine config dir.
const sourceScopeOverlayName = "source-scope.yaml"

// bindMovedFileRescans binds a finished scan into every running workflow run
// whose moved-file gaps it closes, so its findings join the run's inventory.
func (b toolWiring) bindMovedFileRescans(ctx context.Context, completed api.CodeScan) error {
	if b.serveBuilder == nil || b.scanStore == nil || b.workflowStore == nil || completed.Status != api.CodeScanStatusComplete {
		return nil
	}
	runs, err := b.workflowStore.ListRunning(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, run := range runs {
		bound, err := b.scanStore.ListByWorkflowRunID(ctx, run.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(bound) == 0 || bound[0].CanonicalPath != completed.CanonicalPath || !scan.ClosesMovedFiles(bound, completed) {
			continue
		}
		if err := b.scanStore.BindWorkflowRun(ctx, completed.ID, run.ID); err != nil {
			errs = append(errs, fmt.Errorf("bind scan %s to run %s: %w", completed.ID, run.ID, err))
		}
	}
	return errors.Join(errs...)
}

func (b toolWiring) reconcileScanWorkflowTerminals(ctx context.Context, scanID string) error {
	if b.serveBuilder == nil || b.scanStore == nil || b.workflowMgr == nil {
		return nil
	}
	bindings, err := b.scanStore.PendingTerminalWorkflowBindings(ctx, scanID, 256)
	if err != nil {
		return err
	}
	var errs []error
	for _, binding := range bindings {
		if err := b.workflowMgr.RecordObligationTerminal(ctx, binding.WorkflowRunID, scan.WorkflowObligationKind); err != nil {
			errs = append(errs, fmt.Errorf("scan %s workflow %s: %w", binding.ScanID, binding.WorkflowRunID, err))
			continue
		}
		if err := b.scanStore.MarkWorkflowTerminalNotified(ctx, binding.ScanID, binding.WorkflowRunID); err != nil {
			errs = append(errs, fmt.Errorf("acknowledge scan %s workflow %s: %w", binding.ScanID, binding.WorkflowRunID, err))
		}
	}
	return errors.Join(errs...)
}
