package hitl

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestNativeApprovalCapacityCoversAnsweringBeforePublication(t *testing.T) {
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "read production capacity", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "create production memory", err)
	option := CurrentActionOption()
	option.Coverage = strings.Repeat("x", 45<<16)
	projection, release, err := NewNativeAuthorityProjection(t.Context(), memory, []ApprovalOption{option})
	testutil.FailErr(t, "encode a plan that fits creation", err)
	release()
	var capacity *toolcontract.ProgramMemoryLimitError
	if err := admitAuthorityLifecycle(t.Context(), projection, []ApprovalOption{option}); !errors.As(err, &capacity) {
		t.Fatalf("unanswerable plan passed publication admission: %v", err)
	}
	if memory.InUse().State != 0 {
		t.Fatal("publication retained encoder state")
	}
}

func TestNativeAuthorityFailureReleasesEachAdmittedPhase(t *testing.T) {
	original := CurrentActionOption()
	source, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: 64 << 20})
	testutil.FailErr(t, "create encoding ledger", err)
	projection, release, err := NewNativeAuthorityProjection(t.Context(), source, []ApprovalOption{original})
	testutil.FailErr(t, "encode authority fixture", err)
	release()
	for _, maximum := range []int64{128, 4096, 16384, 65536} {
		state, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: maximum, State: maximum})
		testutil.FailErr(t, "create bounded phase ledger", err)
		_, retained, err := RestoreNativeAuthorityProjection(t.Context(), state, projection.Representation(), projection.Digest(), projection.Canonical())
		if err == nil {
			retained()
		}
		if state.InUse().State != 0 {
			t.Fatalf("restore at %d retained %d bytes", maximum, state.InUse().State)
		}
	}
}
