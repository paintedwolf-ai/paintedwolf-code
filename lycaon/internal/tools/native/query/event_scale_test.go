package query

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestQueryStreamingPreservesBoundedMemoryAndReadFailures(t *testing.T) {
	h := newHarness(t, nil)
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: 1 << 20})
	if err != nil {
		t.Fatalf("memory: %v", err)
	}
	h.owners.Revisions.Memory = memory
	input := "[" + strings.Repeat("1,", 10000) + "2]"
	h.exec(input, "jq", "--stream", "select(length == 2 and .[0][0] == 10000) | .[1]").expect(t, "stream selection", "2\n", 0)
	if memory.InUse() != (toolcontract.ProgramMemoryUsage{}) || memory.Peak().Working > 256<<10 {
		t.Fatalf("query memory %+v peak %+v", memory.InUse(), memory.Peak())
	}
	inv := &invocation{ctx: context.Background(), owners: h.owners, input: &queryBytesWithFailure{err: &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED"}}}
	in := &inputs{inv: inv, operands: commandprogram.Fields{{Value: "-"}}, eventMode: true, limit: 1 << 20}
	defer in.close()
	if _, ok := in.Next(); ok || toolrejection.AsToolReject(in.stop) == nil {
		t.Fatalf("read failure became an event: %v", in.stop)
	}
}

func TestSlurpOwnsRetainedInputsAndRefusesOversizedAggregate(t *testing.T) {
	h := newHarness(t, nil)
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: 1 << 20})
	if err != nil {
		t.Fatalf("memory: %v", err)
	}
	h.owners.Revisions.Memory = memory
	h.exec("1 2 3", "jq", "-cs", ".").expect(t, "slurp", "[1,2,3]\n", 0)
	h.owners.limitOverride = map[string]int64{"query_value_bytes": 256}
	result := h.exec(strings.Repeat("1 ", 100), "jq", "-s", ".")
	reject := toolrejection.AsToolReject(result.err)
	if reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" || reject.Data["limit_id"] != "query_value_bytes" {
		t.Fatalf("aggregate refusal: %v", result.err)
	}
	if memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatalf("slurp retained memory %+v", memory.InUse())
	}
}
