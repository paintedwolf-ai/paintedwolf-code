package configuration

import (
	"path/filepath"
	"sort"
	"strings"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
)

// AgentBudgetConfig configures finding caps and ranking for agent feedback.
type AgentBudgetConfig struct {
	MaxHintsPerInjection int      `yaml:"max_hints_per_injection"`
	MaxMessageChars      int      `yaml:"max_message_chars"`
	MaxGuidanceFindings  int      `yaml:"max_guidance_findings"`
	MaxQueryResults      int      `yaml:"max_query_results"`
	MinSeverity          string   `yaml:"min_severity"`
	Prioritize           []string `yaml:"prioritize"`
	DedupeBy             string   `yaml:"dedupe_by"`
	PreferPaths          string   `yaml:"prefer_paths"`
}

// BudgetResult is the output of FindingBudget.Apply.
type BudgetResult struct {
	Findings  []api.SecurityFinding
	Truncated bool
}

// FindingBudget ranks, dedupes, and caps findings before hint resolution.
type FindingBudget struct {
	cfg AgentBudgetConfig
}

// NewFindingBudget constructs a budget from gates config.
func NewFindingBudget(cfg AgentBudgetConfig) *FindingBudget {
	if cfg.MaxGuidanceFindings <= 0 {
		cfg.MaxGuidanceFindings = 500
	}
	if cfg.MaxHintsPerInjection <= 0 {
		cfg.MaxHintsPerInjection = 8
	}
	if cfg.MaxMessageChars <= 0 {
		cfg.MaxMessageChars = 200
	}
	if cfg.MinSeverity == "" {
		cfg.MinSeverity = "medium"
	}
	if len(cfg.Prioritize) == 0 {
		cfg.Prioritize = []string{"error", "warning"}
	}
	if cfg.DedupeBy == "" {
		cfg.DedupeBy = "fingerprint_primary"
	}
	return &FindingBudget{cfg: cfg}
}

// Echo maps the effective budget to the wire summary shape.
func (b *FindingBudget) Echo() *api.ScanAgentBudget {
	return &api.ScanAgentBudget{
		MinSeverity:          b.cfg.MinSeverity,
		MaxHintsPerInjection: b.cfg.MaxHintsPerInjection,
		DedupeBy:             b.cfg.DedupeBy,
	}
}

// MaxHintsPerInjection returns the guidance cap for agent injection.
func (b *FindingBudget) MaxHintsPerInjection() int {
	if b == nil {
		return 8
	}
	return b.cfg.MaxHintsPerInjection
}

// Apply filters, ranks, dedupes, and caps findings.
func (b *FindingBudget) Apply(findings []api.SecurityFinding, touchedPaths []string) BudgetResult {
	if b == nil {
		return BudgetResult{Findings: findings}
	}
	filtered := make([]api.SecurityFinding, 0, len(findings))
	minRank := scanfindings.MinSeverityRank(b.cfg.MinSeverity)
	for _, f := range findings {
		if api.FindingLevelRank(f.Level) <= minRank {
			filtered = append(filtered, f)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		return budgetScore(filtered[i], b.cfg, touchedPaths) < budgetScore(filtered[j], b.cfg, touchedPaths)
	})
	deduped := dedupeFindings(filtered, b.cfg.DedupeBy)
	truncated := false
	if cap := b.cfg.MaxGuidanceFindings; cap > 0 && len(deduped) > cap {
		deduped = deduped[:cap]
		truncated = true
	}
	return BudgetResult{Findings: deduped, Truncated: truncated}
}

func dedupeFindings(in []api.SecurityFinding, by string) []api.SecurityFinding {
	seen := map[string]struct{}{}
	out := make([]api.SecurityFinding, 0, len(in))
	for _, f := range in {
		key := dedupeKey(f, by)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, f)
	}
	return out
}

func dedupeKey(f api.SecurityFinding, by string) string {
	switch by {
	case "fingerprint_primary", "fingerprint", "fingerprints.primary":
		return f.Fingerprints.Primary
	case "rule_id+path", "rule_path":
		return f.RuleID + "\x00" + scanfindings.PrimaryURI(f)
	default:
		return f.RuleID
	}
}

func budgetScore(f api.SecurityFinding, cfg AgentBudgetConfig, touchedPaths []string) int {
	guidanceSev := scanfindings.LevelToGuidanceSeverity(f.Level)
	rank := len(cfg.Prioritize) + 1
	for i, want := range cfg.Prioritize {
		if strings.EqualFold(want, guidanceSev) {
			rank = i
			break
		}
	}
	score := rank * 1000
	if cfg.PreferPaths == "touched_by_leg" && pathTouched(scanfindings.PrimaryURI(f), touchedPaths) {
		score -= 500
	}
	return score
}

func pathTouched(file string, touched []string) bool {
	if file == "" || len(touched) == 0 {
		return false
	}
	cleanFile := filepath.Clean(file)
	for _, p := range touched {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		cleanTouch := filepath.Clean(p)
		if cleanFile == cleanTouch || strings.HasPrefix(cleanFile, cleanTouch+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
