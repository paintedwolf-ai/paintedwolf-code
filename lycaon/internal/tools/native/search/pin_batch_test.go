package search

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native"
)

func TestSearchPinPagesReleaseRecordedMembership(t *testing.T) {
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 4 << 20, State: 4 << 20})
	if err != nil {
		t.Fatalf("memory: %v", err)
	}
	log := &pinLog{}
	inv := &invocation{ctx: t.Context(), owners: &Owners{Pins: log, Revisions: native.ExecutionRevisions{Memory: memory}}, request: commandprogram.OperationRequest{Invocation: toolcontract.OperationInvocation{Identity: toolcontract.ExecutionIdentity{ExecutionID: "pins"}}}}
	for i := range 4097 {
		sha := strings.Repeat("a", 64)
		revision := toolcontract.ExecutionRevision{Role: toolcontract.SourceRevision, Mode: toolcontract.PublishedDiskRevision, Basis: toolcontract.CapturedRevision, RootID: "root", Path: fmt.Sprintf("%d.go", i), DiskSHA256: sha, Representation: toolcontract.OriginalEncodedBytes, ContentSHA256: sha}
		if err := inv.recordPin(revision); err != nil {
			t.Fatalf("record pin %d: %v", i, err)
		}
		if len(inv.pins) >= searchPinBatchSize {
			t.Fatalf("retained complete page: %d", len(inv.pins))
		}
	}
	if err := inv.flushPins(); err != nil {
		t.Fatalf("flush pins: %v", err)
	}
	inv.closePins()
	if len(log.paths("pins")) != 4097 || inv.flushedPins != 4097 || memory.InUse().State != 0 || memory.Peak().State > 2<<20 {
		t.Fatalf("pin pages: count %d ordinal %d memory %+v peak %+v", len(log.paths("pins")), inv.flushedPins, memory.InUse(), memory.Peak())
	}
}
