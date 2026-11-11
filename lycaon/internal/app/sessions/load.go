package sessions

import (
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/toolpolicy"
)

// loadCompactor binds no window: every use takes its config from session limits.
func loadCompactor(svc *llm.Service, tracker cost.CostTracker) (compaction.ContextCompactor, error) {
	if _, err := modelinfo.LoadModelContextWindows(); err != nil {
		return nil, err
	}
	var summarizer compaction.Summarizer
	if svc != nil && svc.Registry != nil && svc.Policy != nil && llm.ProviderUtilityCallsEnabled() {
		summarizer = svc.BindSummarizer(&llm.RegistrySummarizer{
			Cost:    tracker,
			Purpose: "compaction",
			Class:   llm.UtilityClassBackground,
		})
	}
	return compaction.NewSimpleCompactor(compaction.DefaultCompactionConfig(), summarizer), nil
}

func loadProfileRuntimeRules() *toolpolicy.ProfileRuntimeRules {
	rules, err := toolpolicy.LoadProfileRuntimeRules()
	if err == nil {
		return rules
	}
	return nil
}
