package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPlaybookMatcherLoadsFromModuleRoot(t *testing.T) {
	t.Parallel()
	contract, err := prompts.LoadPersonaContract()
	contractcheck.FailErr(t, "prompts.LoadPersonaContract failed", err)
	m, err := prompts.LoadPlaybookMatcherEffective(contract)
	contractcheck.FailErr(t, "prompts.LoadPlaybookMatcherEffective failed", err)
	if m == nil {
		t.Fatal("nil matcher")
	}
}
