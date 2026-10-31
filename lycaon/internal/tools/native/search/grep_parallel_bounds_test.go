package search

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestGrepFileStreamsPreserveMappingsAndPinBeforeOutput(t *testing.T) {
	h := newHarness(t, map[string]string{"a": "needle a\n", "b": "needle b\n"})
	output := pinCheckedGrepOutput{h: h}
	r := parallelFixture(t, h, t.Context(), &output)
	testutil.FailErr(t, "write prefix", r.inv.out.text("prefix\n"))
	output.execution = r.inv.request.Invocation.Identity.ExecutionID
	submitFiles(t, r, 2, "a", "b")
	testutil.FailErr(t, "finish mapped search", r.files.finish(nil))
	if got := output.String(); got != "prefix\na:needle a\nb:needle b\n" {
		t.Fatalf("mapped bytes = %q", got)
	}
	if len(output.mappings) != 2 {
		t.Fatalf("source mappings = %v", output.mappings)
	}
	for i, mapping := range output.mappings {
		expected, ok := h.pins.pins[r.inv.request.Invocation.Identity.ExecutionID][i].SourcePin()
		if !ok || mapping.Source != expected || mapping.OutputStartByte != int64(9+i*11) || mapping.OutputEndByte != int64(17+i*11) || mapping.SourceStartByte != 0 || mapping.SourceEndByte != 8 {
			t.Fatalf("mapping %d = %+v", i, mapping)
		}
	}
}

type pinCheckedGrepOutput struct {
	mappingBuffer
	h         *harness
	execution string
}

func (o *pinCheckedGrepOutput) Write(p []byte) (int, error) {
	if o.execution != "" {
		minimum := 1
		if o.Len() >= 18 {
			minimum = 2
		}
		if got := len(o.h.pins.paths(o.execution)); got < minimum {
			o.h.t.Fatalf("output preceded pins: have %d want %d", got, minimum)
		}
	}
	return o.mappingBuffer.Write(p)
}

func TestGrepFileEventChunksAndCancellationAreBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	events := make(chan grepFileEvent, grepEventWindow)
	buffers := newGrepBuffers(grepEventWindow + 2)
	writer := &grepFileWriter{sink: &grepEventSink{ctx: ctx, events: events, buffers: buffers}}
	defer writer.close()
	finished := make(chan error, 1)
	go func() {
		_, err := writer.Write(bytes.Repeat([]byte("x"), 10*grepOutputChunk))
		finished <- err
	}()
	first := <-events
	if len(first.bytes) != grepOutputChunk {
		t.Fatalf("event bytes = %d", len(first.bytes))
	}
	buffers.put(first.bytes)
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked event writer = %v", err)
	}
	for len(events) > 0 {
		event := <-events
		if len(event.bytes) > grepOutputChunk {
			t.Fatalf("oversized event = %d", len(event.bytes))
		}
		buffers.put(event.bytes)
	}
}

func TestGrepWorkerMemoryAdmissionAndRelease(t *testing.T) {
	for _, limit := range []int64{1, 4 << 20} {
		t.Run(strconv.FormatInt(limit, 10), func(t *testing.T) {
			h := newHarness(t, nil)
			memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: limit, State: 1})
			testutil.FailErr(t, "create memory admission", err)
			h.owners.Revisions.Memory = memory
			r := parallelFixture(t, h, t.Context(), &bytes.Buffer{})
			files, err := newGrepFiles(r, 2)
			if limit == 1 {
				var budget *toolcontract.ProgramMemoryLimitError
				if !errors.As(err, &budget) || memory.InUse().Working != 0 {
					t.Fatalf("worker admission = %v usage=%+v", err, memory.InUse())
				}
				return
			}
			testutil.FailErr(t, "admit workers", err)
			r.files = files
			if memory.InUse().Working == 0 || memory.InUse().Working >= limit {
				t.Fatalf("worker memory charge = %+v", memory.InUse())
			}
			testutil.FailErr(t, "release workers", files.finish(nil))
			if memory.InUse().Working != 0 {
				t.Fatalf("worker memory leaked: %+v", memory.InUse())
			}
		})
	}
}

func TestGrepParallelFilesShareOneOutputLimit(t *testing.T) {
	h := newHarness(t, map[string]string{"a": "needle a\n", "b": "needle b\n"})
	h.owners.StreamBytes = 15
	r := h.exec("", "grep", "needle", "a", "b")
	reject := toolrejection.AsToolReject(r.err)
	if reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" || int64(len(r.stdout)) > h.owners.StreamBytes {
		t.Fatalf("shared output limit: result=%+v error=%v stdout=%q", r.result, r.err, r.stdout)
	}
}

func TestGrepFirstRecordDoesNotWaitForFileCompletion(t *testing.T) {
	events := make(chan grepFileEvent, grepEventWindow)
	buffers := newGrepBuffers(grepEventWindow + 2)
	writer := &grepFileWriter{sink: &grepEventSink{ctx: t.Context(), events: events, buffers: buffers}}
	defer writer.close()
	_, err := writer.Write([]byte("first record\n"))
	testutil.FailErr(t, "write first record", err)
	select {
	case event := <-events:
		if string(event.bytes) != "first record\n" {
			t.Fatalf("first record = %q", event.bytes)
		}
		buffers.put(event.bytes)
	default:
		t.Fatal("first output record waited for file completion")
	}
}

type reusedGrepOutput struct {
	mappingBuffer
	seen   map[*byte]bool
	writes int
}

func (o *reusedGrepOutput) Write(p []byte) (int, error) {
	if len(p) > 0 {
		o.seen[&p[0]] = true
		o.writes++
	}
	return o.mappingBuffer.Write(p)
}

func TestGrepMappedRecordsReuseBoundedTransportStorage(t *testing.T) {
	h := newHarness(t, map[string]string{"a": strings.Repeat("needle a\n", 2000), "b": strings.Repeat("needle b\n", 2000)})
	memory := grepMemory(t, 4<<20)
	h.owners.Revisions.Memory = memory
	output := &reusedGrepOutput{seen: map[*byte]bool{}}
	r := parallelFixture(t, h, t.Context(), output)
	submitFiles(t, r, 2, "a", "b")
	maximum := cap(r.files.buffers.available)
	testutil.FailErr(t, "finish mapped records", r.files.finish(nil))
	if len(output.mappings) != 4000 || output.writes < 4000 || len(output.seen) > maximum || len(output.seen) >= output.writes || memory.InUse().Working != 0 {
		t.Fatalf("mapped transport reuse: mappings=%d writes=%d buffers=%d maximum=%d held=%+v", len(output.mappings), output.writes, len(output.seen), maximum, memory.InUse())
	}
}

func TestGrepOutputRequiresBoundedBufferOwnership(t *testing.T) {
	events := make(chan grepFileEvent, grepEventWindow)
	writer := &grepFileWriter{sink: &grepEventSink{ctx: t.Context(), events: events}}
	defer writer.close()
	if n, err := writer.Write([]byte("record\n")); err == nil || n != 0 || len(events) != 0 {
		t.Fatalf("unowned output: n=%d err=%v events=%d", n, err, len(events))
	}
}
