package toolusage

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

const SchemaVersion = "1"

// Profile is a tool-usage report.
type Profile struct {
	SchemaVersion string `json:"schema_version"`
	Mode          string `json:"mode"` // replay | live
	CaptureDir    string `json:"capture_dir,omitempty"`
	CorpusID      string `json:"corpus_id,omitempty"`
	Runs          int    `json:"runs"`
	Model         string `json:"model,omitempty"`
	ProviderID    string `json:"provider_id,omitempty"`

	ToolCalls            map[string]int            `json:"tool_calls"`
	ToolCallsByAgentType map[string]map[string]int `json:"tool_calls_by_agent_type"`
	ToolCallsBySurface   map[string]map[string]int `json:"tool_calls_by_surface"`

	Read   ReadMetrics   `json:"read"`
	Search SearchMetrics `json:"search"`
	Survey SurveyMetrics `json:"survey"`
	Visual VisualMetrics `json:"visual"`

	TokenSpend  TokenSpend   `json:"token_spend"`
	Cache       CacheMetrics `json:"cache"`
	TaskSuccess TaskSuccess  `json:"task_success"`
	Aggregates  *Aggregates  `json:"aggregates,omitempty"`
}

// SurveyMetrics describes summarize call sequences.
type SurveyMetrics struct {
	SummarizeCalls                  int `json:"summarize_calls"`
	SummarizeBatches                int `json:"summarize_batches"`
	MaxSummariesInBatch             int `json:"max_summaries_in_batch"`
	MixedSurveyBatches              int `json:"mixed_survey_batches"`
	ConsecutiveSummarizeCalls       int `json:"consecutive_summarize_calls"`
	RepeatedSummarizeScopes         int `json:"repeated_summarize_scopes"`
	TargetedInspectionsAfterSummary int `json:"targeted_inspections_after_summary"`
}

// VisualMetrics counts capture attempts, not successful receipts or presentation.
type VisualMetrics struct {
	SealedTerminalCalls       int `json:"sealed_terminal_calls"`
	SealedWithCapabilityCalls int `json:"sealed_with_capability_calls"`
	HeldSnapshotCalls         int `json:"held_snapshot_calls"`
	HeldScreenCalls           int `json:"held_screen_calls"`
	PageCaptureCalls          int `json:"page_capture_calls"`
}

// ReadMetrics covers read whole-file vs scoped and re-read behavior.
type ReadMetrics struct {
	TotalReads     int     `json:"total_reads"`
	WholeFileReads int     `json:"whole_file_reads"`
	ScopedReads    int     `json:"scoped_reads"`
	WholeFileRatio float64 `json:"whole_file_ratio"`
	ReReadPaths    int     `json:"re_read_paths"`
	ReReadRatio    float64 `json:"re_read_ratio"`
}

// SearchMetrics holds search tool cardinality samples.
type SearchMetrics struct {
	GrepCalls             int   `json:"grep_calls"`
	GrepMatchCardinality  []int `json:"grep_match_cardinality"`
	FindCalls             int   `json:"find_calls"`
	GlobCalls             int   `json:"glob_calls"`
	FindResultCardinality []int `json:"find_result_cardinality"`
}

// TokenSpend sums prompt and completion tokens across captured LLM calls.
type TokenSpend struct {
	PromptTokens      int `json:"prompt_tokens"`
	CompletionTokens  int `json:"completion_tokens"`
	MissingUsageCalls int `json:"missing_usage_calls"`
}

// CacheMetrics is the prompt-caching signal carried alongside tool spend.
type CacheMetrics struct {
	CacheReadInputTokens     int     `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens"`
	HitRate                  float64 `json:"hit_rate"`
}

// TaskSuccess summarizes worker/coordinator outcomes derived from the capture.
type TaskSuccess struct {
	AgentsTotal     int  `json:"agents_total"`
	WorkersComplete int  `json:"workers_complete"`
	WorkersFailed   int  `json:"workers_failed"`
	WorkersPartial  int  `json:"workers_partial"`
	CoordinatorSeen bool `json:"coordinator_seen"`
}

// Aggregates holds multi-run statistics for scalar metrics.
type Aggregates struct {
	Runs                    int         `json:"runs"`
	WholeFileRatio          StatSummary `json:"whole_file_ratio"`
	ReReadRatio             StatSummary `json:"re_read_ratio"`
	PromptTokens            StatSummary `json:"prompt_tokens"`
	CompletionTokens        StatSummary `json:"completion_tokens"`
	CacheHitRate            StatSummary `json:"cache_hit_rate"`
	WorkersComplete         StatSummary `json:"workers_complete"`
	SummarizeCalls          StatSummary `json:"summarize_calls"`
	MixedSurveyBatches      StatSummary `json:"mixed_survey_batches"`
	RepeatedSummarizeScopes StatSummary `json:"repeated_summarize_scopes"`
	SealedTerminalCalls     StatSummary `json:"sealed_terminal_calls"`
	HeldSnapshotCalls       StatSummary `json:"held_snapshot_calls"`
	HeldScreenCalls         StatSummary `json:"held_screen_calls"`
	PageCaptureCalls        StatSummary `json:"page_capture_calls"`
}

// StatSummary is aggregate stats for one metric across repeated runs.
type StatSummary struct {
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	StdDev float64 `json:"stddev"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	P50    float64 `json:"p50"`
	P90    float64 `json:"p90"`
	N      int     `json:"n"`
	CV     float64 `json:"coefficient_of_variation"`
}

// EncodeJSON writes a deterministic JSON profile.
func (p Profile) EncodeJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(normalizeProfile(p))
}

func normalizeProfile(p Profile) Profile {
	p.ToolCalls = sortedIntMap(p.ToolCalls)
	p.ToolCallsByAgentType = sortedNestedIntMap(p.ToolCallsByAgentType)
	p.ToolCallsBySurface = sortedNestedIntMap(p.ToolCallsBySurface)
	sort.Ints(p.Search.GrepMatchCardinality)
	sort.Ints(p.Search.FindResultCardinality)
	return p
}

// RenderTable prints a human-readable summary table.
func (p Profile) RenderTable(w io.Writer) {
	fmt.Fprintf(w, "tool-usage profile  mode=%s  schema=%s  runs=%d\n", p.Mode, p.SchemaVersion, p.Runs)
	if p.CaptureDir != "" {
		fmt.Fprintf(w, "capture: %s\n", p.CaptureDir)
	}
	if p.CorpusID != "" {
		fmt.Fprintf(w, "corpus: %s\n", p.CorpusID)
	}
	if p.Model != "" {
		fmt.Fprintf(w, "model: %s  provider: %s\n", p.Model, p.ProviderID)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "tool calls:")
	for _, row := range sortedToolCallRows(p.ToolCalls) {
		fmt.Fprintf(w, "  %-16s %d\n", row.name, row.count)
	}
	fmt.Fprintf(w, "\nread: total=%d whole_file=%d scoped=%d ratio=%.3f re_read_paths=%d re_read_ratio=%.3f\n",
		p.Read.TotalReads, p.Read.WholeFileReads, p.Read.ScopedReads, p.Read.WholeFileRatio,
		p.Read.ReReadPaths, p.Read.ReReadRatio)
	fmt.Fprintf(w, "search: grep=%d find=%d glob=%d\n", p.Search.GrepCalls, p.Search.FindCalls, p.Search.GlobCalls)
	fmt.Fprintf(w, "survey: summarize=%d batches=%d max_batch=%d mixed_batches=%d consecutive=%d repeated_scopes=%d followup_inspections=%d\n",
		p.Survey.SummarizeCalls, p.Survey.SummarizeBatches, p.Survey.MaxSummariesInBatch,
		p.Survey.MixedSurveyBatches, p.Survey.ConsecutiveSummarizeCalls,
		p.Survey.RepeatedSummarizeScopes, p.Survey.TargetedInspectionsAfterSummary)
	fmt.Fprintf(w, "tokens: prompt=%d completion=%d\n", p.TokenSpend.PromptTokens, p.TokenSpend.CompletionTokens)
	fmt.Fprintf(w, "visual attempts (not verdicts): sealed_terminal=%d sealed_with_capability=%d held_snapshot=%d held_screen=%d page_capture=%d\n",
		p.Visual.SealedTerminalCalls, p.Visual.SealedWithCapabilityCalls,
		p.Visual.HeldSnapshotCalls, p.Visual.HeldScreenCalls, p.Visual.PageCaptureCalls)
	fmt.Fprintf(w, "cache: read=%d creation=%d hit_rate=%.3f\n",
		p.Cache.CacheReadInputTokens, p.Cache.CacheCreationInputTokens, p.Cache.HitRate)
	fmt.Fprintf(w, "task: agents=%d workers_complete=%d workers_failed=%d workers_partial=%d\n",
		p.TaskSuccess.AgentsTotal, p.TaskSuccess.WorkersComplete, p.TaskSuccess.WorkersFailed, p.TaskSuccess.WorkersPartial)
	if p.Aggregates != nil && p.Aggregates.Runs > 1 {
		fmt.Fprintf(w, "\naggregates (runs=%d):\n", p.Aggregates.Runs)
		fmt.Fprintf(w, "  whole_file_ratio mean=%.3f stddev=%.3f cv=%.3f\n",
			p.Aggregates.WholeFileRatio.Mean, p.Aggregates.WholeFileRatio.StdDev, p.Aggregates.WholeFileRatio.CV)
		fmt.Fprintf(w, "  cache_hit_rate mean=%.3f stddev=%.3f cv=%.3f\n",
			p.Aggregates.CacheHitRate.Mean, p.Aggregates.CacheHitRate.StdDev, p.Aggregates.CacheHitRate.CV)
	}
}

type toolCallRow struct {
	name  string
	count int
}

func sortedToolCallRows(m map[string]int) []toolCallRow {
	if len(m) == 0 {
		return nil
	}
	names := sortedKeys(m)
	out := make([]toolCallRow, len(names))
	for i, name := range names {
		out[i] = toolCallRow{name: name, count: m[name]}
	}
	return out
}

func sortedIntMap(m map[string]int) map[string]int {
	if m == nil {
		return map[string]int{}
	}
	out := make(map[string]int, len(m))
	for _, k := range sortedKeys(m) {
		out[k] = m[k]
	}
	return out
}

func sortedNestedIntMap(m map[string]map[string]int) map[string]map[string]int {
	if m == nil {
		return map[string]map[string]int{}
	}
	outer := sortedKeys(m)
	out := make(map[string]map[string]int, len(outer))
	for _, k := range outer {
		out[k] = sortedIntMap(m[k])
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// CompareProfiles returns true when normalized JSON encodings match.
func CompareProfiles(a, b Profile) bool {
	ja, errA := json.Marshal(normalizeProfile(a))
	jb, errB := json.Marshal(normalizeProfile(b))
	if errA != nil || errB != nil {
		// A profile that will not encode is not evidence of equality.
		return false
	}
	return string(ja) == string(jb)
}

// DiffSummary returns a short human diff when profiles differ.
func DiffSummary(a, b Profile) string {
	var parts []string
	if a.Read.WholeFileRatio != b.Read.WholeFileRatio {
		parts = append(parts, fmt.Sprintf("whole_file_ratio %.3f -> %.3f", a.Read.WholeFileRatio, b.Read.WholeFileRatio))
	}
	if a.TokenSpend.PromptTokens != b.TokenSpend.PromptTokens {
		parts = append(parts, fmt.Sprintf("prompt_tokens %d -> %d", a.TokenSpend.PromptTokens, b.TokenSpend.PromptTokens))
	}
	if a.Cache.HitRate != b.Cache.HitRate {
		parts = append(parts, fmt.Sprintf("cache_hit_rate %.3f -> %.3f", a.Cache.HitRate, b.Cache.HitRate))
	}
	if len(parts) == 0 {
		return "profiles differ (see JSON)"
	}
	return strings.Join(parts, "; ")
}
