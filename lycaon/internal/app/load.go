package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func resolveServeAPIToken() (token string, generated bool, err error) {
	if token = strings.TrimSpace(os.Getenv("LYCAON_API_TOKEN")); token != "" {
		return token, false, nil
	}
	token, err = api.ResolveAPIToken()
	if err != nil {
		return "", false, err
	}
	return token, true, nil
}

func loadSettingsService(configRoot string) (*settings.Service, string, error) {
	if configRoot != "" {
		svc, err := settings.NewService()
		if err != nil {
			return nil, "", fmt.Errorf("settings service: %w", err)
		}
		return svc, configRoot, nil
	}
	root := configlayout.FindModuleRoot()
	svc, err := settings.NewService()
	if err != nil {
		return nil, root, fmt.Errorf("settings service: %w", err)
	}
	return svc, root, nil
}

func loadToolRuntime(settingsSvc *settings.Service, configRoot string, catalog *extpacks.EffectiveCatalog, activation tools.SchemaActivation, resolve tools.RequestResolver, record tools.RequestObserver, lookup tools.SkillLookup, rerank decide.Reranker) (*toolhost.Runtime, error) {
	cfg := toolhost.RuntimeConfig{ConfigRoot: configRoot, Catalog: catalog, Activation: activation, RequestResolver: resolve, RequestObserver: record, SkillLookup: lookup, Rerank: rerank}
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

func loadCompactor(svc *llm.Service, configRoot string, tracker cost.CostTracker) (compaction.ContextCompactor, error) {
	// Compaction settings are bundled config, so there is no per-root variant
	// to load and no not-exist case to fall back from.
	cfg := compaction.DefaultCompactionConfig()

	windows, winErr := modelinfo.LoadModelContextWindows()
	if winErr != nil {
		return nil, winErr
	}
	var policy llm.ModelPolicy
	var lookup llm.ContextLengthLookup
	if svc != nil {
		if svc.Policy != nil {
			var err error
			policy, err = svc.Policy.Get(llm.SettingsScopeGlobal, "")
			if err != nil {
				return nil, err
			}
		}
		if svc.Registry != nil {
			lookup = svc.Registry
		}
	}
	applied, _, err := llm.ApplyLiveBudget(cfg, policy, lookup, windows)
	if err != nil {
		return nil, err
	}
	cfg = applied

	summarizer := compaction.Summarizer(compaction.TruncateSummarizer{})
	if svc != nil && svc.Registry != nil && svc.Policy != nil && llm.ProviderUtilityCallsEnabled() {
		summarizer = svc.BindSummarizer(&llm.RegistrySummarizer{
			Fallback: compaction.TruncateSummarizer{},
			Cost:     tracker,
			Purpose:  "compaction",
		})
	}
	return compaction.NewSimpleCompactor(cfg, summarizer), nil
}

func loadProfileRuntimeRules() *toolpolicy.ProfileRuntimeRules {
	rules, err := toolpolicy.LoadProfileRuntimeRules()
	if err == nil {
		return rules
	}
	return nil
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
