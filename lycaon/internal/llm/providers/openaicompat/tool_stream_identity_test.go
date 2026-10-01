package openaicompat

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestParallelToolStreamsUseIDsWhenIndicesAreAbsent(t *testing.T) {
	acc := map[int]*providerwire.StreamTool{}
	testutil.FailErr(t, "start parallel calls", mergeStreamToolDeltas(acc, []toolCallWire{
		{ID: "a", Function: functionCallWire{Name: "read", Arguments: `{"path":`}},
		{ID: "b", Function: functionCallWire{Name: "read", Arguments: `{"path":`}},
	}))
	before := providerwire.CollectToolCalls(acc, false, false)
	testutil.FailErr(t, "finish calls out of order", mergeStreamToolDeltas(acc, []toolCallWire{
		{ID: "b", Function: functionCallWire{Arguments: `"b.py"}`}},
		{ID: "a", Function: functionCallWire{Arguments: `"a.py"}`}},
	}))
	calls := providerwire.CollectToolCalls(acc, false, true)
	if len(calls) != 2 || calls[0].WireID != "a" || calls[1].WireID != "b" || calls[0].Args["path"] != "a.py" || calls[1].Args["path"] != "b.py" {
		t.Fatalf("parallel calls were combined or reordered: %+v", calls)
	}
	if calls[0].ID == calls[1].ID || calls[0].ID != before[0].ID || calls[1].ID != before[1].ID {
		t.Fatal("host call identities changed between deltas")
	}
}

func TestAmbiguousToolStreamFragmentsAreRejected(t *testing.T) {
	zero, one := 0, 1
	for name, fragment := range map[string]toolCallWire{
		"no identity":   {Function: functionCallWire{Arguments: `}`}},
		"changed index": {Index: &one, ID: "a"},
		"changed id":    {Index: &zero, ID: "different"},
	} {
		t.Run(name, func(t *testing.T) {
			acc := map[int]*providerwire.StreamTool{}
			testutil.FailErr(t, "start calls", mergeStreamToolDeltas(acc, []toolCallWire{{Index: &zero, ID: "a"}, {Index: &one, ID: "b"}}))
			if err := mergeStreamToolDeltas(acc, []toolCallWire{fragment}); err == nil {
				t.Fatal("ambiguous fragment was assigned to a tool")
			}
		})
	}
}

func TestSingleToolStreamCanContinueWithoutIdentity(t *testing.T) {
	acc := map[int]*providerwire.StreamTool{}
	testutil.FailErr(t, "start call", mergeStreamToolDeltas(acc, []toolCallWire{{ID: "a", Function: functionCallWire{Name: "read", Arguments: `{"path":`}}}))
	testutil.FailErr(t, "continue only call", mergeStreamToolDeltas(acc, []toolCallWire{{Function: functionCallWire{Arguments: `"a.py"}`}}}))
	calls := providerwire.CollectToolCalls(acc, false, true)
	if len(calls) != 1 || calls[0].Args["path"] != "a.py" {
		t.Fatalf("single call changed: %+v", calls)
	}
}
