package execution

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythonevidence"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
)

type Runtime struct {
	Host *toolhost.Runtime
	// PrivateGit is the one manager worker Git tools and worker workspaces share.
	PrivateGit  *privategit.Manager
	Registry    *tools.ExecutorRegistry
	TurnLoads   *turnload.Ledger
	TurnSources *TurnSources
	Hints       *guidance.HintConfig
	Rejections  *guidance.StaticRejectFormatter
}

func Build(settings *settings.Service, moduleRoot string, catalog *extpacks.EffectiveCatalog, rerank decide.Reranker, privateGit *privategit.Manager) (Runtime, error) {
	runtime := Runtime{PrivateGit: privateGit}
	runtime.TurnLoads = turnload.NewLedger()
	runtime.TurnSources = &TurnSources{}
	var err error
	runtime.Host, err = loadToolRuntime(settings, moduleRoot, catalog, runtime.TurnLoads, runtime.TurnSources, rerank, privateGit)
	if err != nil {
		return runtime, fmt.Errorf("tool runtime: %w", err)
	}
	return runtime, nil
}

func (runtime *Runtime) LoadGuidance() error {
	var err error
	runtime.Hints, runtime.Rejections, err = loadStockHintRegistry()
	if err != nil {
		return fmt.Errorf("hint registry: %w", err)
	}
	if runtime.Rejections != nil {
		runtime.Host.Authority.ApplyGuidanceRejects(runtime.Rejections)
	}
	schemas, err := loadToolSchemas()
	if err != nil {
		return fmt.Errorf("tool schemas: %w", err)
	}
	runtime.Host.Executor.Metadata.SetToolSchemas(schemas)
	runtime.Registry = tools.NewExecutorRegistry(runtime.Host.Executor, runtime.Host.Registry)
	return nil
}
func loadToolRuntime(settingsSvc *settings.Service, configRoot string, catalog *extpacks.EffectiveCatalog, activation tools.SchemaActivation, sources *TurnSources, rerank decide.Reranker, privateGit *privategit.Manager) (*toolhost.Runtime, error) {
	cfg := toolhost.RuntimeConfig{ConfigRoot: configRoot, Catalog: catalog, Activation: activation, Rerank: rerank, RequestResolver: sources.resolve, RequestObserver: sources.record, SkillLookup: sources.lookup, PrivateGit: privateGit}
	if settingsSvc != nil {
		cfg.Approvals = settingsSvc.Approvals
	}
	if cfg.ConfigRoot == "" {
		cfg.ConfigRoot = configlayout.FindModuleRoot()
	}
	if pySvc, pyErr := buildPortablePythonService(context.Background()); pyErr == nil {
		cfg.PortablePython = pySvc
	}
	rt, err := toolhost.NewRuntime(cfg)
	if err != nil {
		return nil, fmt.Errorf("native tool runtime: %w", err)
	}
	return rt, nil
}

// buildPortablePythonService constructs the production Portable Python service
// with Painted Wolf's real secretmatch.Matcher screener and disk-backed
// blobstore RetainedArtifactStore (never test defaults).
func buildPortablePythonService(ctx context.Context) (*portablepython.Service, error) {
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	if err != nil {
		return nil, fmt.Errorf("build secretmatch matcher: %w", err)
	}
	screener, err := pythonevidence.NewHostSecretScreener(matcher)
	if err != nil {
		return nil, fmt.Errorf("create host secret screener: %w", err)
	}
	retainedRoot, err := configdir.Root()
	if err != nil || retainedRoot == "" {
		retainedRoot = os.TempDir()
	}
	blobStore := blobstore.Store{
		Root: retainedRoot,
		Dir:  filepath.Join("portable-python", "retained"),
	}
	streamPort := portablepython.NewBlobRetainedStreamPort(blobStore)
	retainedStore, err := pythonevidence.NewProductionRetainedArtifactStore(pythonevidence.RetainedArtifactConfig{
		Screener:   screener,
		StreamPort: streamPort,
	})
	if err != nil {
		return nil, fmt.Errorf("create production retained artifact store: %w", err)
	}
	contStore, err := pythonadmission.NewFileContinuationStore(filepath.Join(retainedRoot, "portable-python", "approvals"), "continuation.json")
	if err != nil {
		return nil, fmt.Errorf("create durable continuation store: %w", err)
	}
	broker := pythonadmission.NewApprovalBroker(pythonadmission.ApprovalBrokerConfig{
		Store: contStore,
	})
	_, _ = broker.ReconcileOnStartup(ctx, "host restarted while guest was parked")

	receiptStore, err := portablepython.NewFileReceiptStore(filepath.Join(retainedRoot, "portable-python", "receipts"))
	if err != nil {
		return nil, fmt.Errorf("create durable receipt store: %w", err)
	}
	return portablepython.NewService(ctx, portablepython.ServiceConfig{
		ScratchRoot:    filepath.Join(retainedRoot, "portable-python", "scratch"),
		Broker:         broker,
		SecretScreener: screener,
		RetainedStore:  retainedStore,
		StreamPort:     streamPort,
		ReceiptSink:    receiptStore,
	})
}

func loadToolSchemas() (*toolschema.Config, error) {
	eff := extpacks.Active()
	if eff == nil {
		return nil, fmt.Errorf("tool schemas: effective catalog required")
	}
	cfg, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func loadStockHintRegistry() (*guidance.HintConfig, *guidance.StaticRejectFormatter, error) {
	cfg, err := guidance.LoadHintConfigStock()
	if err != nil {
		return nil, nil, err
	}
	return cfg, guidance.NewStaticRejectFormatter(cfg), nil
}
