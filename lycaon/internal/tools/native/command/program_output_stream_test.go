package command

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

type outputSpillAccount struct{ held int64 }

func (a *outputSpillAccount) ReserveSpill(_ context.Context, n int64) (func(), error) {
	a.held += n
	return func() { a.held -= n }, nil
}

func TestProgramStreamPreservesBytesAndSnapshotBeyondFormerCeiling(t *testing.T) {
	account := &outputSpillAccount{}
	stream := newProgramStream(t.Context(), 32<<20, account, nil)
	defer stream.Close()
	chunk := bytes.Repeat([]byte("test-result\n"), 4096)
	var written int64
	for written < 20<<20 {
		n, err := stream.Write(chunk)
		testutil.FailErr(t, "stage output", err)
		written += int64(n)
	}
	snapshot := stream.snapshot(t)
	testutil.FailErr(t, "write after snapshot", func() error { _, err := stream.Write([]byte("PASS\n")); return err }())
	n, err := io.Copy(io.Discard, snapshot)
	testutil.FailErr(t, "read snapshot", err)
	if n != written || stream.Len() != written+5 {
		t.Fatalf("snapshot=%d written=%d total=%d", n, written, stream.Len())
	}
	testutil.FailErr(t, "close output", stream.Close())
	if account.held != 0 {
		t.Fatalf("spill reservation leaked %d", account.held)
	}
}

func TestProgramStreamHardLimitRetainsExactPrefix(t *testing.T) {
	account := &outputSpillAccount{}
	stream := newProgramStream(t.Context(), 8, account, nil)
	defer stream.Close()
	_, err := stream.Write([]byte("prefix\n"))
	testutil.FailErr(t, "write prefix", err)
	n, err := stream.Write([]byte("excess\n"))
	var limit *commandprogram.StreamLimitError
	if n != 0 || toolrejection.AsToolReject(err) == nil || toolrejection.AsToolReject(err).Code != "COMMAND_LIMIT_EXCEEDED" || !errors.As(err, &limit) || limit.Limit != "spill_bytes" || !stream.truncated {
		t.Fatalf("overflow=(%d,%v)", n, err)
	}
	got, err := io.ReadAll(stream.snapshot(t))
	testutil.FailErr(t, "read prefix", err)
	if string(got) != "prefix\n" {
		t.Fatalf("retained prefix=%q", got)
	}
}

func TestOutputWindowDoesNotSplitLongLinesAndNamesIncompleteCopy(t *testing.T) {
	data := strings.Repeat("x", 20000) + "\nsummary\n"
	sample, err := sampleOutput(strings.NewReader(data), 12<<10)
	testutil.FailErr(t, "sample long line", err)
	rendered, window, parts, err := sample.window(t.Context(), "stdout", "output:fixture", "@output/fixture.stdout", false)
	testutil.FailErr(t, "render long line", err)
	if window.HeadEndByte != 0 || window.TailStartLine != 2 || window.OmittedLines != 1 || window.Complete || !strings.HasSuffix(rendered, "summary\n") || strings.Contains(rendered, "full output:") {
		t.Fatalf("window=%+v rendered=%q", window, rendered)
	}
	for _, part := range parts {
		if strings.Contains(string(part.data), "x") {
			t.Fatal("partial source line delivered")
		}
	}
}

func (s *programStream) snapshot(t *testing.T) io.Reader {
	t.Helper()
	reader, _, err := s.snapshotInput()
	testutil.FailErr(t, "snapshot output", err)
	return reader
}

func TestProgramStreamReservesOnlyReachedStagingCapacity(t *testing.T) {
	account := &outputSpillAccount{}
	stream := newProgramStream(t.Context(), 256<<20, account, nil)
	defer stream.Close()
	_, err := stream.Write([]byte("small output"))
	testutil.FailErr(t, "stage small output", err)
	if account.held != 1<<20 {
		t.Fatalf("reserved=%d for small output", account.held)
	}
}

func TestChildMappingsFollowInterleavedWrites(t *testing.T) {
	account := &outputSpillAccount{}
	stream := newProgramStream(t.Context(), 1024, account, testMappingMemory(t))
	defer stream.Close()
	first, second := newChildStreamView(stream), newChildStreamView(stream)
	defer first.Close()
	defer second.Close()
	for _, write := range []struct {
		view *childStreamView
		text string
	}{{first, "aa"}, {second, "bb"}, {first, "cc"}} {
		_, err := write.view.Write([]byte(write.text))
		testutil.FailErr(t, "interleaved write", err)
	}
	mapping := toolcontract.StreamSourceMapping{OutputStartByte: 0, OutputEndByte: 4, SourceStartByte: 10, SourceEndByte: 14}
	testutil.FailErr(t, "project first writer", first.RecordSourceMapping(mapping))
	got := stream.mappings.values
	if len(got) != 2 || got[0].OutputStartByte != 0 || got[0].OutputEndByte != 2 || got[1].OutputStartByte != 4 || got[1].SourceStartByte != 12 {
		t.Fatalf("interleaved mapping=%+v", got)
	}
}

func TestProgramStreamSessionQuotaProducesHostLimit(t *testing.T) {
	account := newSessionOutputResources()
	account.limit = 16
	stream := newProgramStream(t.Context(), 32, account, nil)
	defer stream.Close()
	_, err := stream.Write([]byte("output"))
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" || reject.Data["limit_id"] != "session_spill_bytes" || !stream.truncated {
		t.Fatalf("quota rejection=%v", err)
	}
}

func TestNoticeReportsExactWindowAndContinuation(t *testing.T) {
	for _, complete := range []bool{false, true} {
		sample, err := sampleOutput(strings.NewReader("one\ntwo\nthree\nfour\n"), 12)
		testutil.FailErr(t, "sample notice fixture", err)
		rendered, window, _, err := sample.window(t.Context(), "stdout", "output:fixture", "@output/fixture.stdout", complete)
		testutil.FailErr(t, "render notice fixture", err)
		label := "full output:"
		if !complete {
			label = "saved output is incomplete:"
		}
		total := "4 lines (19 bytes); "
		if !complete {
			total = "saved " + total
		}
		want := "one\n... 2 lines (10 bytes) omitted from " + total + label + " @output/fixture.stdout (sed -n '2,3p' @output/fixture.stdout) ...\nfour\n"
		if rendered != want || window.HeadEndLine != 1 || window.TailStartLine != 4 || window.OmittedBytes != 10 || window.Complete != complete {
			t.Fatalf("output=%q window=%+v", rendered, window)
		}
	}
}

func TestConcurrentEqualBytesRetainOnlyTheirWriterSource(t *testing.T) {
	stream := newProgramStream(t.Context(), 4096, &outputSpillAccount{}, testMappingMemory(t))
	defer stream.Close()
	var workers sync.WaitGroup
	for _, source := range []string{"disk:a/file", "disk:b/file"} {
		workers.Go(func() {
			view := newChildStreamView(stream)
			defer view.Close()
			for range 50 {
				if _, err := view.Write([]byte("xx")); err != nil {
					t.Error(err)
					return
				}
			}
			mapping := toolcontract.StreamSourceMapping{Source: toolcontract.SourcePin{SourceID: source}, OutputEndByte: 100, SourceEndByte: 100}
			if err := view.RecordSourceMapping(mapping); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	covered := make([]string, 200)
	counts := map[string]int{}
	for _, mapping := range stream.mappings.values {
		if mapping.OutputEndByte-mapping.OutputStartByte != mapping.SourceEndByte-mapping.SourceStartByte {
			t.Fatalf("changed span: %+v", mapping)
		}
		for pos := mapping.OutputStartByte; pos < mapping.OutputEndByte; pos++ {
			if covered[pos] != "" {
				t.Fatalf("byte %d attributed to both %s and %s", pos, covered[pos], mapping.Source.SourceID)
			}
			covered[pos] = mapping.Source.SourceID
			counts[mapping.Source.SourceID]++
		}
	}
	if counts["disk:a/file"] != 100 || counts["disk:b/file"] != 100 {
		t.Fatalf("source byte counts=%v", counts)
	}
}

func testMappingMemory(t *testing.T) *toolcontract.ProgramMemory {
	t.Helper()
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 32 << 20, State: 64 << 20})
	testutil.FailErr(t, "mapping memory", err)
	return memory
}

func TestInterleavedAttributionAdmissionPreservesAllOutput(t *testing.T) {
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: 2048})
	testutil.FailErr(t, "bounded mapping memory", err)
	account := &outputSpillAccount{}
	stream := newProgramStream(t.Context(), 4096, account, memory)
	defer stream.Close()
	first, second := newChildStreamView(stream), newChildStreamView(stream)
	defer first.Close()
	defer second.Close()
	for i := 0; i < 100; i++ {
		for n, view := range []*childStreamView{first, second} {
			data := []byte{byte('A' + n)}
			_, err := view.Write(data)
			testutil.FailErr(t, "interleaved bytes", err)
			pin := toolcontract.SourcePin{SourceID: "disk:r1/" + strings.Repeat("path", 64) + string(data), Revision: "sha256:" + strings.Repeat("a", 64), Representation: toolcontract.UTF8SourceBytes, SHA256: strings.Repeat("a", 64), EndByte: 100}
			testutil.FailErr(t, "optional attribution", view.RecordSourceMapping(toolcontract.StreamSourceMapping{Source: pin, OutputStartByte: int64(i), OutputEndByte: int64(i + 1), SourceStartByte: int64(i), SourceEndByte: int64(i + 1)}))
		}
	}
	got, err := io.ReadAll(stream.snapshot(t))
	testutil.FailErr(t, "read complete output", err)
	snapshot, err := stream.snapshotMappings(t.Context(), int64(len(got)))
	testutil.FailErr(t, "snapshot optional mappings", err)
	defer snapshot.Close()
	if string(got) != strings.Repeat("AB", 100) || len(snapshot.Mappings) != 0 || memory.Peak().State > 2048 {
		t.Fatalf("output=%d mappings=%d memory=%+v", len(got), len(snapshot.Mappings), memory.Peak())
	}
	first.Close()
	second.Close()
	testutil.FailErr(t, "close bounded stream", stream.Close())
	if memory.InUse().State != 0 || account.held != 0 {
		t.Fatalf("attribution/storage leaked %+v disk=%d", memory.InUse(), account.held)
	}
}

func TestNestedEqualBytesUseTheirActualWriteCoordinates(t *testing.T) {
	stream := newProgramStream(t.Context(), 4096, &outputSpillAccount{}, testMappingMemory(t))
	defer stream.Close()
	outer := newChildStreamView(stream)
	defer outer.Close()
	first, second := newChildStreamView(outer), newChildStreamView(outer)
	defer first.Close()
	defer second.Close()
	for _, writer := range []io.Writer{outer, first, second} {
		_, err := writer.Write([]byte("same"))
		testutil.FailErr(t, "nested bytes", err)
	}
	for n, view := range []*childStreamView{second, first} {
		pin := toolcontract.SourcePin{SourceID: []string{"disk:r1/second", "disk:r1/first"}[n], Revision: "sha256:" + strings.Repeat("a", 64), Representation: toolcontract.UTF8SourceBytes, SHA256: strings.Repeat("a", 64), EndByte: 4}
		testutil.FailErr(t, "nested mapping", view.RecordSourceMapping(toolcontract.StreamSourceMapping{Source: pin, OutputEndByte: 4, SourceEndByte: 4}))
	}
	got := stream.mappings.values
	if len(got) != 2 || got[0].OutputStartByte != 8 || got[0].OutputEndByte != 12 || got[1].OutputStartByte != 4 || got[1].OutputEndByte != 8 {
		t.Fatalf("equal bytes acquired wrong coordinates: %+v", got)
	}
	bytes, err := io.ReadAll(stream.snapshot(t))
	testutil.FailErr(t, "nested output", err)
	if string(bytes) != "samesamesame" {
		t.Fatalf("nested bytes=%q", bytes)
	}
}
