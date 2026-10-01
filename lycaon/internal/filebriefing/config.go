package filebriefing

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/config"
	"gopkg.in/yaml.v3"
)

const maxGeneratedTextChars = 8192

// Config defines file briefing prompts and bounds.
type Config struct {
	Version      int              `yaml:"version"`
	Task         string           `yaml:"task"`
	SystemPrompt string           `yaml:"system_prompt"`
	Material     MaterialConfig   `yaml:"material"`
	Generation   GenerationConfig `yaml:"generation"`
	Stream       StreamConfig     `yaml:"stream"`
	Retention    RetentionConfig  `yaml:"retention"`
}

type MaterialConfig struct {
	PromptBudgetTokens    int `yaml:"prompt_budget_tokens"`
	HeaderBudgetTokens    int `yaml:"header_budget_tokens"`
	EnvelopeReserveTokens int `yaml:"envelope_reserve_tokens"`
	MinimumPackTokens     int `yaml:"minimum_pack_tokens"`
	HeaderLines           int `yaml:"header_lines"`
	SymbolWindowLines     int `yaml:"symbol_window_lines"`
	OutlineSymbols        int `yaml:"outline_symbols"`
}

type GenerationConfig struct {
	MaxOutputTokens int `yaml:"max_output_tokens"`
	MaxOutputChars  int `yaml:"max_output_chars"`
	TimeoutMS       int `yaml:"timeout_ms"`
	MinPartialChars int `yaml:"min_partial_chars"`
}

type StreamConfig struct {
	ChunkChars int `yaml:"chunk_chars"`
	FlushMS    int `yaml:"flush_ms"`
}

type RetentionConfig struct {
	RevisionsPerFile int   `yaml:"revisions_per_file"`
	ProjectBytes     int64 `yaml:"project_bytes"`
	DeviceBytes      int64 `yaml:"device_bytes"`
	DeviceRows       int   `yaml:"device_rows"`
	MaintenanceBatch int   `yaml:"maintenance_batch"`
}

func LoadConfig() (Config, error) {
	raw, err := config.Read(config.FileBriefing)
	if err != nil {
		return Config{}, fmt.Errorf("read file briefing config: %w", err)
	}
	out, err := parseConfig(raw)
	if err != nil {
		return Config{}, fmt.Errorf("parse file briefing config: %w", err)
	}
	return out, nil
}

func parseConfig(raw []byte) (Config, error) {
	var out Config
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&out); err != nil {
		return Config{}, err
	}
	if err := out.validate(); err != nil {
		return Config{}, err
	}
	return out, nil
}

func (c Config) validate() error {
	if c.Version < 1 {
		return fmt.Errorf("file briefing config: version must be positive")
	}
	if strings.TrimSpace(c.Task) == "" || strings.TrimSpace(c.SystemPrompt) == "" {
		return fmt.Errorf("file briefing config: task and system_prompt are required")
	}
	values := []struct {
		name  string
		value int
	}{
		{"material.prompt_budget_tokens", c.Material.PromptBudgetTokens},
		{"material.header_budget_tokens", c.Material.HeaderBudgetTokens},
		{"material.envelope_reserve_tokens", c.Material.EnvelopeReserveTokens},
		{"material.minimum_pack_tokens", c.Material.MinimumPackTokens},
		{"material.header_lines", c.Material.HeaderLines},
		{"material.symbol_window_lines", c.Material.SymbolWindowLines},
		{"material.outline_symbols", c.Material.OutlineSymbols},
		{"generation.max_output_tokens", c.Generation.MaxOutputTokens},
		{"generation.max_output_chars", c.Generation.MaxOutputChars},
		{"generation.timeout_ms", c.Generation.TimeoutMS},
		{"generation.min_partial_chars", c.Generation.MinPartialChars},
		{"stream.chunk_chars", c.Stream.ChunkChars},
		{"stream.flush_ms", c.Stream.FlushMS},
	}
	for _, item := range values {
		if item.value <= 0 {
			return fmt.Errorf("file briefing config: %s must be positive", item.name)
		}
	}
	if err := c.Retention.validate(); err != nil {
		return err
	}
	if c.Generation.MaxOutputChars > maxGeneratedTextChars {
		return fmt.Errorf("file briefing config: generation.max_output_chars exceeds wire limit")
	}
	materialFloor := c.Material.HeaderBudgetTokens + c.Material.EnvelopeReserveTokens + c.Material.MinimumPackTokens
	if materialFloor > c.Material.PromptBudgetTokens {
		return fmt.Errorf("file briefing config: material budgets exceed prompt_budget_tokens")
	}
	return nil
}

func (c RetentionConfig) validate() error {
	values := []struct {
		name  string
		value int
	}{
		{"revisions_per_file", c.RevisionsPerFile},
		{"device_rows", c.DeviceRows},
		{"maintenance_batch", c.MaintenanceBatch},
	}
	for _, item := range values {
		if item.value <= 0 {
			return fmt.Errorf("file briefing retention: %s must be positive", item.name)
		}
	}
	if c.ProjectBytes <= 0 || c.DeviceBytes <= 0 {
		return fmt.Errorf("file briefing retention: byte budgets must be positive")
	}
	if c.ProjectBytes > c.DeviceBytes {
		return fmt.Errorf("file briefing retention: project_bytes must not exceed device_bytes")
	}
	return nil
}

func (c Config) GenerationTimeout() time.Duration {
	return time.Duration(c.Generation.TimeoutMS) * time.Millisecond
}

func (c Config) StreamFlushInterval() time.Duration {
	return time.Duration(c.Stream.FlushMS) * time.Millisecond
}
