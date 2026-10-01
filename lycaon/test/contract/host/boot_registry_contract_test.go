package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func TestBootToolClaimsHonestAfterServeMirror(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	if err := tools.ValidateBootToolClaimsHonest(reg); err != nil {
		contractcheck.FailErr(t, "validate boot tool claims match registered handlers", err)
	}
}
