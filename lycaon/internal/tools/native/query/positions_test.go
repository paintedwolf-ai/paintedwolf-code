package query

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	nativejq "github.com/lycaon/lycaon/internal/tools/native/jq"
)

type queryBytesWithFailure struct {
	err  error
	sent bool
}

func (r *queryBytesWithFailure) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.EOF
	}
	r.sent = true
	return copy(p, "0 "), r.err
}

func TestQueryReadRefusalCannotBecomeSuccessfulBufferedValue(t *testing.T) {
	h := newHarness(t, nil)
	in := &inputs{inv: &invocation{ctx: context.Background(), owners: h.owners}, limit: 1024, label: "input"}
	defer in.close()
	refused := &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED"}
	in.stream = nativejq.NewBoundedValueStream(t.Context(), queryInputReader{input: &queryBytesWithFailure{err: refused}, owner: in}, nativejq.DecodeLimits{MaximumDepth: 128, MaximumScalarBytes: 1024}, nil, 1<<20)
	if _, ok := in.nextValue(); ok || !errors.Is(in.stop, refused) {
		t.Fatalf("buffered value escaped refusal: ok %v stop %v", ok, in.stop)
	}
}

func TestQueryRawRecordAndAggregateSlurpLimitsRelease(t *testing.T) {
	for _, test := range []struct {
		words   []string
		input   string
		limit   string
		maximum int64
	}{
		{[]string{"jq", "-R", "."}, "12345\n", "record_bytes", 4},
		{[]string{"jq", "-Rs", "."}, "12345", "whole_file_bytes", 4},
	} {
		h := newHarness(t, nil)
		memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: 1 << 20})
		testutil.FailErr(t, "toolcontract.NewProgramMemory failed", err)
		h.owners.Revisions.Memory = memory
		h.owners.limitOverride = map[string]int64{test.limit: test.maximum}
		result := h.exec(test.input, test.words...)
		result.refused(t, test.limit, "COMMAND_LIMIT_EXCEEDED")
		if result.stdout != "" {
			t.Fatalf("refused raw input escaped: %q", result.stdout)
		}
		if held := memory.InUse(); held != (toolcontract.ProgramMemoryUsage{}) {
			t.Fatalf("raw leases remain %+v", held)
		}
	}
	h := newHarness(t, map[string]string{"a": "123", "b": "456"})
	h.owners.limitOverride = map[string]int64{"whole_file_bytes": 5}
	h.exec("", "jq", "-Rs", ".", "a", "b").refused(t, "aggregate raw inputs", "COMMAND_LIMIT_EXCEEDED")
	h.exec("a\n\nlast", "jq", "-R", ".").expect(t, "raw lines", "\"a\"\n\"\"\n\"last\"\n", 0)
}
