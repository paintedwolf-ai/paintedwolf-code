package literal

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestRevisionMembershipUsesFixedPagesAndExactKeys(t *testing.T) {
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 64 << 20, State: 64 << 20})
	if err != nil {
		t.Fatalf("memory: %v", err)
	}
	index := revisionIndex{}
	for i := range 16384 {
		revision := toolcontract.ExecutionRevision{RootID: "root", Path: fmt.Sprintf("internal/pkg/%05d.go", i), DocumentRevision: int64(i)}
		seen, err := index.remember(t.Context(), memory, revision)
		if err != nil || seen {
			t.Fatalf("remember %d: seen %v error %v", i, seen, err)
		}
		seen, err = index.remember(t.Context(), memory, revision)
		if err != nil || !seen {
			t.Fatalf("repeat %d: seen %v error %v", i, seen, err)
		}
		revision.DocumentRevision++
		seen, err = index.remember(t.Context(), memory, revision)
		if err != nil || seen {
			t.Fatalf("changed revision %d: seen %v error %v", i, seen, err)
		}
	}
	if memory.Peak().State > 40<<20 {
		t.Fatalf("membership peak %+v", memory.Peak())
	}
	index.close()
	index.close()
	if memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatalf("retained membership: %+v", memory.InUse())
	}
}
