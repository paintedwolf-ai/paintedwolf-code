package llm

import (
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

// MockConfig is the on-disk mock_llm.yaml shape.
type MockConfig struct {
	Responses    []MockResponseEntry `yaml:"responses"`
	VisionModels []string            `yaml:"vision_models,omitempty"`
}

// MockResponseEntry is one pattern-based mock completion rule.
type MockResponseEntry struct {
	Pattern      string         `yaml:"pattern"`
	Text         string         `yaml:"text,omitempty"`
	FollowUpText string         `yaml:"follow_up_text,omitempty"`
	AlwaysTool   bool           `yaml:"always_tool,omitempty"`
	ToolCalls    []MockToolCall `yaml:"tool_calls,omitempty"`
}

// MockToolCall is a tool call stub in mock config.
type MockToolCall struct {
	ID   string         `yaml:"id"`
	Name string         `yaml:"name"`
	Args map[string]any `yaml:"args"`
}

// LoadMockConfig reads the shipped mock LLM responses.
func LoadMockConfig() (*MockConfig, error) {
	data, err := config.Read(config.MockLLM)
	if err != nil {
		return nil, err
	}
	var cfg MockConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// MockEnabled reports whether the runtime may use the mock LLM client.
// Mock is test-only: explicit LYCAON_LLM_MOCK=1 or an injected test client.
func MockEnabled(testClient modelcall.LLMClient) bool {
	if testClient != nil {
		return true
	}
	return MockOnlyFromEnv()
}

// ProviderUtilityCallsEnabled keeps background utility work off configured providers in test modes.
func ProviderUtilityCallsEnabled() bool {
	return !MockOnlyFromEnv() && !ManualOnlyFromEnv()
}
