package llm

import (
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"gopkg.in/yaml.v3"
)

// NewTestRegistrySummarizer wires a stub lite provider for contract and integration tests.
func NewTestRegistrySummarizer(provider modelcall.Provider) (*RegistrySummarizer, error) {
	dir, err := os.MkdirTemp("", "curator-test-policy")
	if err != nil {
		return nil, err
	}
	policyPath := filepath.Join(dir, "model-policy.yaml")
	data, err := yaml.Marshal(ModelPolicy{
		Lite: ModelRef{ProviderID: provider.ID(), Model: "lite"},
	})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(policyPath, data, 0o600); err != nil {
		return nil, err
	}
	policy, err := NewPolicyStoreAt(policyPath)
	if err != nil {
		return nil, err
	}
	reg := newEmptyRegistry()
	if err := reg.Register(provider); err != nil {
		return nil, err
	}
	return &RegistrySummarizer{
		Registry: reg,
		Policy:   policy,
		Scope:    SettingsScopeGlobal,
	}, nil
}
