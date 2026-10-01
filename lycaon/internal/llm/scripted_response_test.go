package llm

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStreamCollectionPreservesHostScriptedProvenance(t *testing.T) {
	for _, progress := range []bool{false, true} {
		stream := make(chan modelcall.StreamChunk, 1)
		stream <- modelcall.StreamChunk{Content: "Prepared.", Scripted: true, Done: true}
		close(stream)
		var completion *modelcall.Completion
		var err error
		if progress {
			completion, _, err = modelcall.CollectStreamWithProgress(stream, nil)
		} else {
			completion, _, err = modelcall.CollectStream(stream)
		}
		testutil.FailErr(t, "collect scripted response", err)
		if !completion.Scripted {
			t.Fatal("host-scripted response lost provenance")
		}
	}
}
