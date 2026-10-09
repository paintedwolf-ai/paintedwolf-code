package execution

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
)

type Runtime struct {
	Host       *toolhost.Runtime
	Registry   *tools.ExecutorRegistry
	TurnLoads  *turnload.Ledger
	Hints      *guidance.HintConfig
	Rejections *guidance.StaticRejectFormatter
}

func Build(settings *settings.Service, moduleRoot string, catalog *extpacks.EffectiveCatalog, rerank decide.Reranker) (Runtime, error) {
	var runtime Runtime
	runtime.TurnLoads = turnload.NewLedger()
	var err error
	runtime.Host, err = loadToolRuntime(settings, moduleRoot, catalog, runtime.TurnLoads, rerank)
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
func loadToolRuntime(settingsSvc *settings.Service, configRoot string, catalog *extpacks.EffectiveCatalog, activation tools.SchemaActivation, rerank decide.Reranker) (*toolhost.Runtime, error) {
	cfg := toolhost.RuntimeConfig{ConfigRoot: configRoot, Catalog: catalog, Activation: activation, Rerank: rerank}
	if settingsSvc != nil {
		cfg.Approvals = settingsSvc.Approvals
	}
	if cfg.ConfigRoot == "" {
		cfg.ConfigRoot = configlayout.FindModuleRoot()
	}
	rt, err := toolhost.NewRuntime(cfg)
	if err != nil {
		return nil, fmt.Errorf("native tool runtime: %w", err)
	}
	return rt, nil
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
