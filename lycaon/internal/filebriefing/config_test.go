package filebriefing

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := LoadConfig()
	testutil.FailErr(t, "load file briefing config", err)
	return cfg
}

func TestConfigRejectsUnknownFields(t *testing.T) {
	_, err := parseConfig([]byte(`version: 1
task: explain the file
system_prompt: explain source code
material:
  prompt_budget_tokens: 1800
  header_budget_tokens: 320
  envelope_reserve_tokens: 260
  minimum_pack_tokens: 200
  header_lines: 48
  symbol_window_lines: 14
  outline_symbols: 32
generation:
  max_output_tokens: 256
  max_output_chars: 8192
  timeout_ms: 20000
  min_partial_chars: 120
stream:
  chunk_chars: 48
  flush_ms: 100
  retry_count: 2
retention:
  revisions_per_file: 4
  project_bytes: 8388608
  device_bytes: 33554432
  device_rows: 10000
  maintenance_batch: 250
`))
	if err == nil || !strings.Contains(err.Error(), "retry_count") {
		t.Fatalf("parseConfig error = %v", err)
	}
}

func TestBundledConfigDefinesInteractiveBounds(t *testing.T) {
	cfg := testConfig(t)
	if cfg.Version < 1 {
		t.Fatalf("config version = %d", cfg.Version)
	}
	if cfg.Material.PromptBudgetTokens >= 2200 {
		t.Fatalf("prompt budget = %d, want below general summarize budget", cfg.Material.PromptBudgetTokens)
	}
	if cfg.Generation.MaxOutputTokens > 256 {
		t.Fatalf("output budget = %d, want an interactive briefing", cfg.Generation.MaxOutputTokens)
	}
	if cfg.Generation.MaxOutputChars > maxGeneratedTextChars {
		t.Fatalf("output characters = %d", cfg.Generation.MaxOutputChars)
	}
	if got := cfg.GenerationTimeout(); got <= 0 {
		t.Fatalf("generation timeout = %v", got)
	}
	if cfg.Retention.ProjectBytes >= cfg.Retention.DeviceBytes {
		t.Fatalf("retention hierarchy = %+v", cfg.Retention)
	}
}

func TestConfigRejectsGeneratedTextOverWireLimit(t *testing.T) {
	cfg := testConfig(t)
	cfg.Generation.MaxOutputChars = maxGeneratedTextChars + 1
	err := cfg.validate()
	if err == nil || !strings.Contains(err.Error(), "wire limit") {
		t.Fatalf("validate error = %v", err)
	}
}

func TestConfigRejectsMaterialBudgetOverflow(t *testing.T) {
	cfg := testConfig(t)
	materialFloor := cfg.Material.HeaderBudgetTokens + cfg.Material.EnvelopeReserveTokens + cfg.Material.MinimumPackTokens
	cfg.Material.PromptBudgetTokens = materialFloor - 1
	err := cfg.validate()
	if err == nil || !strings.Contains(err.Error(), "material budgets exceed") {
		t.Fatalf("validate error = %v", err)
	}
}
