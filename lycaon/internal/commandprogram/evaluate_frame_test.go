package commandprogram

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestIsolatedFrameRefusesPrefixAdmissionBeforeDispatchFork(t *testing.T) {
	const entryBytes = int64(variableBytes + len("NAME") + len("value"))
	for _, tc := range []struct {
		name     string
		capacity int64
	}{
		{"assignment", entryBytes - 1},
		{"export replacement", entryBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newEvalFixture(t)
			memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1, State: tc.capacity})
			testutil.FailErr(t, "create prefix admission memory", err)
			fixture.ports.Memory = memory
			evaluation := &evaluation{ports: fixture.ports, programName: "prefix"}
			parent, closeParent, err := evaluation.rootFrame(t.Context())
			testutil.FailErr(t, "create parent frame", err)
			defer closeParent()
			parent.prefix = map[string]string{"NAME": "value"}
			child, release, err := parent.isolate(t.Context(), nil)
			if release != nil {
				defer release()
			}
			var refusal *toolcontract.ProgramMemoryLimitError
			if !errors.As(err, &refusal) || refusal.Account != "state" || child != nil || release != nil {
				t.Fatalf("prefix %s admitted a frame: child=%v release=%t err=%v", tc.name, child, release != nil, err)
			}
			if fixture.log.forks != 0 || memory.InUse().State != 0 {
				t.Fatalf("refused prefix escaped admission: forks=%d memory=%+v", fixture.log.forks, memory.InUse())
			}
		})
	}
}
