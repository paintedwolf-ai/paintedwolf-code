package compaction

import (
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/limits"
)

// Defaults fill omitted budget fields.
const (
	defaultBudgetTriggerPct        = 60
	defaultHardCeilingPct          = 85
	defaultTargetTokensPct         = 35
	defaultSummaryInputTokens      = 8192
	defaultSummaryRetryInputTokens = 3072
	defaultSummaryMessageTokens    = 512
	defaultSummaryOutputTokens     = 1536
)

// CompactionConfig is the on-disk compaction.yaml shape.
type CompactionConfig struct {
	Enabled                          bool   `yaml:"enabled"`
	ModelContextWindow               int    `yaml:"model_context_window"`
	LiveBudgetPct                    int    `yaml:"live_budget_pct"`
	WindowSource                     string `yaml:"window_source"`
	BudgetTriggerPct                 int    `yaml:"budget_trigger_pct"`
	HardCeilingPct                   int    `yaml:"hard_ceiling_pct"`
	TargetTokensPct                  int    `yaml:"target_tokens_pct"`
	HardCeilingTokens                int    `yaml:"hard_ceiling_tokens"`
	PruneProtectTailMessages         int    `yaml:"prune_protect_tail_messages"`
	ChunkProtectedToolTokenThreshold int    `yaml:"chunk_protected_tool_token_threshold"`
	ChunkProtectedTailTokenBudget    int    `yaml:"chunk_protected_tail_token_budget"`
	ChunkProtectedMinSavingsTokens   int    `yaml:"chunk_protected_min_savings_tokens"`
	TargetTokens                     int    `yaml:"target_tokens"`
	SessionMinSavingsTokens          int    `yaml:"session_min_savings_tokens"`
	MinSavingsPct                    int    `yaml:"min_savings_pct"`
	MessageSlice                     int    `yaml:"message_slice"`
	KeepRecentMessages               int    `yaml:"keep_recent_messages"`
	ChunkTokenThreshold              int    `yaml:"chunk_token_threshold"`
	ChunkMinSavingsTokens            int    `yaml:"chunk_min_savings_tokens"`
	ChunkTargetTokens                int    `yaml:"chunk_target_tokens"`
	ChunkMaxPerPass                  int    `yaml:"chunk_max_per_pass"`
	ChunkStrategyDefault             string `yaml:"chunk_strategy_default"`
	SummaryInputTokens               int    `yaml:"summary_input_tokens"`
	SummaryRetryInputTokens          int    `yaml:"summary_retry_input_tokens"`
	SummaryMessageTokens             int    `yaml:"summary_message_tokens"`
	SummaryOutputTokens              int    `yaml:"summary_output_tokens"`
	MaxWorkerSummaryChars            int    `yaml:"max_worker_summary_chars"`
	MaxCitationGroundingRetries      int    `yaml:"max_citation_grounding_retries"`
	MaxWorkerGroundingRetries        int    `yaml:"max_worker_grounding_retries"`
	MaxGroundingRejectsPerTurn       int    `yaml:"max_grounding_rejects_per_turn"`
	MaxGroundingRejectsPerCycle      int    `yaml:"max_grounding_rejects_per_cycle"`
	MaxToolTurnsAfterCitationReject  int    `yaml:"max_tool_turns_after_citation_reject"`
	MaxReportDocumentRetries         int    `yaml:"max_report_document_retries"`
}

// WindowSource values for CompactionConfig.WindowSource.
const (
	WindowSourceAuto     = "auto"
	WindowSourceExplicit = "explicit"
)

// DefaultLiveBudgetPct is live_budget = true_window × pct / 100 when YAML omits or zeros the field.
const DefaultLiveBudgetPct = 75

var (
	bundledCompactionOnce sync.Once
	bundledCompaction     CompactionConfig
	bundledCompactionErr  error
)

// DefaultCompactionConfig panics if the bundled configuration cannot load.
func DefaultCompactionConfig() CompactionConfig {
	bundledCompactionOnce.Do(func() {
		bundledCompaction, bundledCompactionErr = bundledCompactionConfig()
	})
	if bundledCompactionErr != nil {
		panic(bundledCompactionErr)
	}
	return bundledCompaction
}

// WorkerChildCompactionConfig tightens fit and tail-chunk knobs for worker children.
func WorkerChildCompactionConfig(base CompactionConfig) CompactionConfig {
	out := base
	if out.ModelContextWindow > 0 {
		out.HardCeilingTokens = out.ModelContextWindow * 55 / 100
	} else if out.HardCeilingTokens > 0 {
		out.HardCeilingTokens = out.HardCeilingTokens * 55 / 100
	}
	if out.PruneProtectTailMessages <= 0 || out.PruneProtectTailMessages > 4 {
		out.PruneProtectTailMessages = 4
	}
	if out.KeepRecentMessages <= 0 || out.KeepRecentMessages > 16 {
		out.KeepRecentMessages = 16
	}
	return out
}

func bundledCompactionConfig() (CompactionConfig, error) {
	data, err := config.Read(config.Compaction)
	if err != nil {
		return CompactionConfig{}, fmt.Errorf("read compaction config: %w", err)
	}
	return decodeCompactionConfig(data, CompactionConfig{})
}

func decodeCompactionConfig(data []byte, base CompactionConfig) (CompactionConfig, error) {
	cfg := base
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return CompactionConfig{}, fmt.Errorf("parse compaction config: %w", err)
	}
	return NormalizeConfig(cfg)
}

func NormalizeConfig(cfg CompactionConfig) (CompactionConfig, error) {
	if cfg.SessionMinSavingsTokens == 0 {
		cfg.SessionMinSavingsTokens = 256
	}
	if cfg.MinSavingsPct == 0 {
		cfg.MinSavingsPct = 10
	}
	if cfg.SessionMinSavingsTokens < 0 || cfg.MinSavingsPct < 1 || cfg.MinSavingsPct > 100 {
		return CompactionConfig{}, fmt.Errorf("compaction config: savings require positive tokens and a percentage from 1 to 100")
	}
	if cfg.ModelContextWindow <= 0 {
		return CompactionConfig{}, fmt.Errorf("compaction config: model_context_window must be positive")
	}
	if cfg.LiveBudgetPct <= 0 {
		cfg.LiveBudgetPct = DefaultLiveBudgetPct
	}
	switch src := strings.TrimSpace(cfg.WindowSource); src {
	case "":
		cfg.WindowSource = WindowSourceAuto
	case WindowSourceAuto, WindowSourceExplicit:
		cfg.WindowSource = src
	default:
		return CompactionConfig{}, fmt.Errorf("compaction config: window_source must be %q or %q", WindowSourceAuto, WindowSourceExplicit)
	}
	if cfg.BudgetTriggerPct <= 0 {
		cfg.BudgetTriggerPct = defaultBudgetTriggerPct
	}
	if cfg.TargetTokensPct <= 0 {
		cfg.TargetTokensPct = defaultTargetTokensPct
	}
	if cfg.PruneProtectTailMessages <= 0 {
		return CompactionConfig{}, fmt.Errorf("compaction config: prune_protect_tail_messages must be positive")
	}
	if cfg.ChunkProtectedToolTokenThreshold <= 0 {
		return CompactionConfig{}, fmt.Errorf("compaction config: chunk_protected_tool_token_threshold must be positive")
	}
	if cfg.ChunkProtectedTailTokenBudget <= 0 {
		return CompactionConfig{}, fmt.Errorf("compaction config: chunk_protected_tail_token_budget must be positive")
	}
	if cfg.ChunkProtectedMinSavingsTokens <= 0 {
		return CompactionConfig{}, fmt.Errorf("compaction config: chunk_protected_min_savings_tokens must be positive")
	}
	if cfg.MessageSlice <= 0 {
		return CompactionConfig{}, fmt.Errorf("compaction config: message_slice must be positive")
	}
	if cfg.KeepRecentMessages <= 0 {
		return CompactionConfig{}, fmt.Errorf("compaction config: keep_recent_messages must be positive")
	}
	if cfg.ChunkTokenThreshold <= 0 {
		return CompactionConfig{}, fmt.Errorf("compaction config: chunk_token_threshold must be positive")
	}
	if cfg.ChunkMinSavingsTokens <= 0 {
		return CompactionConfig{}, fmt.Errorf("compaction config: chunk_min_savings_tokens must be positive")
	}
	if cfg.ChunkTargetTokens <= 0 {
		return CompactionConfig{}, fmt.Errorf("compaction config: chunk_target_tokens must be positive")
	}
	if cfg.ChunkMaxPerPass <= 0 {
		return CompactionConfig{}, fmt.Errorf("compaction config: chunk_max_per_pass must be positive")
	}
	if cfg.SummaryInputTokens == 0 {
		cfg.SummaryInputTokens = defaultSummaryInputTokens
	}
	if cfg.SummaryInputTokens < 0 {
		return CompactionConfig{}, fmt.Errorf("compaction config: summary_input_tokens must be positive")
	}
	if cfg.SummaryRetryInputTokens == 0 {
		cfg.SummaryRetryInputTokens = defaultSummaryRetryInputTokens
	}
	if cfg.SummaryRetryInputTokens < 0 || cfg.SummaryRetryInputTokens >= cfg.SummaryInputTokens {
		return CompactionConfig{}, fmt.Errorf("compaction config: summary_retry_input_tokens must be positive and less than summary_input_tokens")
	}
	if cfg.SummaryMessageTokens == 0 {
		cfg.SummaryMessageTokens = defaultSummaryMessageTokens
	}
	if cfg.SummaryMessageTokens < 0 || cfg.SummaryMessageTokens > cfg.SummaryRetryInputTokens {
		return CompactionConfig{}, fmt.Errorf("compaction config: summary_message_tokens must be positive and no greater than summary_retry_input_tokens")
	}
	if cfg.SummaryOutputTokens == 0 {
		cfg.SummaryOutputTokens = defaultSummaryOutputTokens
	}
	if cfg.SummaryOutputTokens < 0 {
		return CompactionConfig{}, fmt.Errorf("compaction config: summary_output_tokens must be positive")
	}
	if cfg.ChunkStrategyDefault == "" {
		cfg.ChunkStrategyDefault = "summarize"
	}
	// ApplyLiveBudget fills an omitted worker summary cap from the live window.
	if cfg.MaxCitationGroundingRetries <= 0 {
		cfg.MaxCitationGroundingRetries = limits.DefaultCitationGroundingRetries
	}
	if cfg.MaxWorkerGroundingRetries <= 0 {
		cfg.MaxWorkerGroundingRetries = limits.DefaultWorkerGroundingRetries
	}
	deriveTokenBudgets(&cfg)
	if cfg.ModelContextWindow > 0 && cfg.HardCeilingPct > 0 {
		triggerPct := cfg.BudgetTriggerPct
		if triggerPct <= 0 {
			triggerPct = defaultBudgetTriggerPct
		}
		if cfg.HardCeilingPct < triggerPct {
			triggerTokens := cfg.ModelContextWindow * triggerPct / 100
			if cfg.HardCeilingTokens > 0 && cfg.HardCeilingTokens < triggerTokens {
				cfg.HardCeilingTokens = triggerTokens
			}
		}
	}
	return cfg, nil
}

// CompactionTriggerTokens shares the session trigger with the context indicator.
// It returns zero when the model window is unknown.
func (c CompactionConfig) CompactionTriggerTokens() int {
	if c.ModelContextWindow <= 0 {
		return 0
	}
	pct := c.BudgetTriggerPct
	if pct <= 0 {
		pct = defaultBudgetTriggerPct
	}
	trigger := c.ModelContextWindow * pct / 100
	if c.HardCeilingTokens > 0 && c.HardCeilingTokens < trigger {
		return c.HardCeilingTokens
	}
	return trigger
}

// ShouldCompactSession reports whether estimated tokens exceed the budget trigger or hard ceiling.
func ShouldCompactSession(estimatedTokens, modelWindow int, cfg CompactionConfig) bool {
	if cfg.HardCeilingTokens > 0 && estimatedTokens >= cfg.HardCeilingTokens {
		return true
	}
	pct := cfg.BudgetTriggerPct
	if pct <= 0 {
		pct = defaultBudgetTriggerPct
	}
	if modelWindow <= 0 {
		return false
	}
	return estimatedTokens*100 >= modelWindow*pct
}

// deriveTokenBudgets fills HardCeilingTokens and TargetTokens from model_context_window
// when not explicitly set. hard_ceiling_pct defaults to defaultHardCeilingPct.
func deriveTokenBudgets(cfg *CompactionConfig) {
	if cfg == nil || cfg.ModelContextWindow <= 0 {
		return
	}
	ceilingPct := cfg.HardCeilingPct
	if ceilingPct <= 0 {
		ceilingPct = defaultHardCeilingPct
	}
	if cfg.HardCeilingTokens <= 0 {
		cfg.HardCeilingTokens = cfg.ModelContextWindow * ceilingPct / 100
	}
	if cfg.TargetTokens <= 0 {
		cfg.TargetTokens = cfg.ModelContextWindow * cfg.TargetTokensPct / 100
	}
}
