package query

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	nativejq "github.com/lycaon/lycaon/internal/tools/native/jq"
)

func TestQueryPositionsPreserveLineAndColumnAcrossChunkBoundaries(t *testing.T) {
	data := []byte("first\né😀\nlast value")
	for _, chunk := range []int{1, 2, 7, 8, 63} {
		h := newHarness(t, nil)
		inv := &invocation{ctx: t.Context(), owners: h.owners}
		positions, err := newInputPositions(inv, 1024, "input")
		testutil.FailErr(t, "newInputPositions failed", err)
		for start := 0; start < len(data); start += chunk {
			if _, err = positions.Write(data[start:min(start+chunk, len(data))]); err != nil {
				testutil.FailErr(t, "positions.Write failed", err)
			}
		}
		for offset := int64(0); offset <= int64(len(data)); offset++ {
			wantLine, wantColumn := lineColumn(data, offset)
			line, column := positions.lineColumn(offset)
			end := offset
			if next := bytes.IndexByte(data[offset:], '\n'); next >= 0 {
				end += int64(next) + 1
			} else {
				end = int64(len(data))
			}
			wantCurrent := bytes.Count(data[:end], []byte{'\n'})
			if line != wantLine || column != wantColumn || positions.lineAt(offset) != wantCurrent {
				t.Fatalf("chunk %d offset %d line/column %d/%d want %d/%d current %d want %d", chunk, offset, line, column, wantLine, wantColumn, positions.lineAt(offset), wantCurrent)
			}
		}
		positions.close()
	}
}

func TestQueryPositionsAdmitCompactMetadataAndRelease(t *testing.T) {
	h := newHarness(t, nil)
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: 1 << 20})
	testutil.FailErr(t, "toolcontract.NewProgramMemory failed", err)
	h.owners.Revisions.Memory = memory
	positions, err := newInputPositions(&invocation{ctx: t.Context(), owners: h.owners}, 64<<20, "input")
	testutil.FailErr(t, "newInputPositions failed", err)
	chunk := []byte(strings.Repeat("x", (64<<10)-1) + "\n")
	for range 64 {
		if _, err = positions.Write(chunk); err != nil {
			testutil.FailErr(t, "positions.Write failed", err)
		}
	}
	if len(positions.buffer.Bytes) != 512<<10 || memory.InUse().State > 512<<10 {
		t.Fatalf("body retained: %d metadata, held %+v", len(positions.buffer.Bytes), memory.InUse())
	}
	positions.close()
	if memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatalf("position leases remain %+v", memory.InUse())
	}
}

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
	positions, err := newInputPositions(in.inv, in.limit, in.label)
	testutil.FailErr(t, "newInputPositions failed", err)
	in.positions = positions
	defer in.close()
	refused := &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED"}
	in.stream = nativejq.NewValueStream(queryInputReader{input: &queryBytesWithFailure{err: refused}, owner: in})
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
