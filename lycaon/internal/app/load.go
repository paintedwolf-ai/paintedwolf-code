package app

import (
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/toolpolicy"
)

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
