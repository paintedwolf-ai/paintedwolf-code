package scanning

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/scan"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanexecution "github.com/lycaon/lycaon/internal/scan/execution"
	scanregistry "github.com/lycaon/lycaon/internal/scan/registry"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/pkg/api"
)

// SurfaceGate determines whether a root path is subject to scan configurations.
type SurfaceGate interface {
	AppliesPath(ctx context.Context, rootPath string) bool
	FilterPaths(ctx context.Context, rootPaths []string) []string
}

// Runtime holds the wired scanning infrastructure and cadence services.
type Runtime struct {
	Store       *scan.SQLStore
	Coordinator *scan.CoordinatorImpl
	Cadence     *scancadence.Service
	Closeout    *scan.SecurityCloseoutChecker
	Obligation  *scan.WorkflowObligation
	Guidance    *scan.SessionGuidanceAdapter
	Registry    scan.CodeScannerRegistry
	Runner      *scanexecution.Runner
	Triggers    *scan.TriggerService
	GatesCfg    scancfg.GatesConfig
	Scopes      *sourcescope.Provider
}

// Dependencies specifies external inputs required to bootstrap scanning services.
type Dependencies struct {
	Database              *db.Store
	DataDir               string
	ModuleRoot            string
	Events                *eventoutbox.Outbox
	Publisher             *events.Publisher
	GitMgr                *git.Manager
	Snapshots             *sourcesnapshot.Store
	Settings              *settings.Service
	Projects              project.Registry
	SurfaceGate           SurfaceGate
	EvidenceStore         inspector.EvidenceStore
	SimpleInspector       *inspector.SimpleInspector
	FingerprintScannerKey func() []byte
	ScanIgnores           scan.SecretIgnoreSource
	InjectRenderer        *prompts.InjectRenderer
	TestRegistry          scan.CodeScannerRegistry
	DelegationBySession   func(string) (string, string, bool)
	WorkflowRunsGet       func(context.Context, string) (*api.WorkflowRun, error)
	WorkflowRunParams     func(context.Context, string, string, string) (map[string]any, error)
	OnDelta               func(context.Context, api.CodeScan, []api.SecurityFinding, []api.SecurityFinding)
	OnScanDone            func(context.Context, api.CodeScan)
	RetryCloseout         func(context.Context, string) error
	WorkflowTerminals     func(context.Context, string) error
	BindMovedFiles        func(context.Context, api.CodeScan) error
}

// Build creates the scanning runtime, obligations, cadence, and runner.
func Build(ctx context.Context, deps Dependencies) (*Runtime, error) {
	gatesCfg := scancfg.DefaultGatesConfig()
	store := scan.NewSQLStore(deps.Database)
	store.SetEventOutbox(deps.Events)
	store.SecretIgnores = deps.ScanIgnores

	coord := scan.NewCoordinator(store, deps.GitMgr, deps.Snapshots)
	if deps.Settings != nil {
		coord.Settings = deps.Settings.SecurityScanners
	}

	closeout := &scan.SecurityCloseoutChecker{
		Store:    store,
		Evidence: deps.EvidenceStore,
	}
	if deps.Settings != nil {
		closeout.Settings = deps.Settings.SecurityScanners
	}

	obligation := &scan.WorkflowObligation{
		Ledger:   store,
		History:  store,
		Runs:     deps.WorkflowRunsGet,
		Params:   deps.WorkflowRunParams,
		Projects: func(c context.Context, projectID string) (string, error) {
			p, err := deps.Projects.Get(c, projectID)
			if err != nil {
				return "", err
			}
			return project.PrimaryRootPath(p), nil
		},
	}
	if deps.Settings != nil {
		obligation.Settings = deps.Settings.SecurityScanners
	}

	var appliesPath func(context.Context, string) bool
	var filterPaths func(context.Context, []string) []string
	if deps.SurfaceGate != nil {
		appliesPath = deps.SurfaceGate.AppliesPath
		filterPaths = deps.SurfaceGate.FilterPaths
	}

	scopes, err := loadSourceScope(deps.DataDir, appliesPath, deps.Snapshots)
	if err != nil {
		return nil, err
	}

	scannerReg := deps.TestRegistry
	runnerCfg := scancfg.DefaultRunnerConfig()
	if scannerReg == nil {
		var scannerKey []byte
		if deps.FingerprintScannerKey != nil {
			scannerKey = deps.FingerprintScannerKey()
		}
		reg, regErr := scanregistry.New(scanregistry.Options{
			ScannerFingerprintKey: scannerKey,
			ModuleRoot:            deps.ModuleRoot,
			ProcessPriority:       runnerCfg.ExecProcessPriority(),
			ProjectTierApplies:    appliesPath,
		})
		if regErr != nil {
			return nil, fmt.Errorf("scan registry: %w", regErr)
		}
		scannerReg = reg
	}
	coord.Registry = scannerReg

	scanIngester := &scan.IngesterImpl{
		SecretIgnores:           deps.ScanIgnores,
		Inspector:               deps.SimpleInspector,
		Module:                  scancfg.DefaultModuleConfig(),
		Budget:                  scancfg.NewFindingBudget(gatesCfg.Gates.AgentBudget),
		BlockOn:                 gatesCfg.Gates.BlockOn,
		OverlayRootsApply:       filterPaths,
		RecordWithoutDelegation: true,
	}

	runner := scanexecution.NewRunner(store, scannerReg, scanIngester, runnerCfg, deps.Publisher)
	runner.DataDir = deps.DataDir
	runner.Snapshots = deps.Snapshots
	runner.Coordinator = coord
	if deps.Settings != nil {
		runner.Settings = deps.Settings.SecurityScanners
	}
	runner.OnDelta = deps.OnDelta

	triggers := &scan.TriggerService{
		Coordinator: coord,
		Registry:    scannerReg,
		Gates:       gatesCfg,
	}
	if deps.Settings != nil {
		triggers.Settings = deps.Settings.SecurityScanners
	}

	var cadence *scancadence.Service
	if deps.Settings != nil {
		cadence = scancadence.New(store, coord, scannerReg, deps.Settings.SecurityScanners, gatesCfg, triggers)
		cadence.OverlayRootsApply = filterPaths
		cadence.Preempt = runner.Preempt
		cadence.Scopes = scopes
		cadence.ObserveRepochange()
	}

	obligation.Triggers = triggers
	obligation.Full = cadence

	runner.OnTerminal = func(c context.Context, completed api.CodeScan) {
		if cadence != nil {
			cadence.OnTerminal(c, completed)
		}
		if deps.OnScanDone != nil {
			deps.OnScanDone(c, completed)
		}
		if deps.BindMovedFiles != nil {
			_ = deps.BindMovedFiles(c, completed)
		}
		if deps.WorkflowTerminals != nil {
			_ = deps.WorkflowTerminals(c, completed.ID)
		}
		if completed.DelegationID != "" && deps.RetryCloseout != nil {
			_ = deps.RetryCloseout(c, completed.DelegationID)
		}
	}
	runner.ReconcileTerminal = func(c context.Context) error {
		if deps.WorkflowTerminals != nil {
			return deps.WorkflowTerminals(c, "")
		}
		return nil
	}

	scanProvider := scan.NewGuidanceProvider(coord, gatesCfg.Gates.AgentBudget)
	scanProvider.SetInjectRenderer(deps.InjectRenderer)
	guidance := &scan.SessionGuidanceAdapter{
		Provider:            scanProvider,
		DelegationBySession: deps.DelegationBySession,
	}

	return &Runtime{
		Store:       store,
		Coordinator: coord,
		Cadence:     cadence,
		Closeout:    closeout,
		Obligation:  obligation,
		Guidance:    guidance,
		Registry:    scannerReg,
		Runner:      runner,
		Triggers:    triggers,
		GatesCfg:    gatesCfg,
		Scopes:      scopes,
	}, nil
}

func loadSourceScope(dataDir string, appliesPath func(context.Context, string) bool, snapshots *sourcesnapshot.Store) (*sourcescope.Provider, error) {
	cfg, err := sourcescope.LoadConfig(filepath.Join(dataDir, "source-scope.yaml"))
	if err != nil {
		return nil, fmt.Errorf("source scope: %w", err)
	}
	provider, err := sourcescope.NewProvider(cfg, appliesPath)
	if err != nil {
		return nil, fmt.Errorf("source scope: %w", err)
	}
	if snapshots != nil {
		snapshots.SetScopes(provider)
	}
	sourcecatalog.Process().SetScopes(provider)
	return provider, nil
}
