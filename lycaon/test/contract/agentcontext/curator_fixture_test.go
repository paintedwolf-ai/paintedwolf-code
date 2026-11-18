package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/policytest"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func newCuratorFixture(t *testing.T, provider modelcall.Provider) *llm.RegistrySummarizer {
	t.Helper()
	registry := &llm.Registry{}
	registry.SetOutboundSecretScreen(llm.NewModelSecretScreen(nil, nil))
	contractcheck.FailErr(t, "register curator", registry.Register(provider))
	return &llm.RegistrySummarizer{
		Registry: registry,
		Policy:   policytest.New(t, llm.ModelPolicy{Lite: llm.ModelRef{ProviderID: provider.ID(), Model: "lite"}}),
		Scope:    llm.SettingsScopeGlobal,
	}
}
