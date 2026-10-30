package textsearch

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func admittedMatcher(t *testing.T, memory *toolcontract.ProgramMemory, input int64) *Matcher {
	t.Helper()
	limits := catalogLimits(t)
	limits.InputBytes, limits.Reserve = input, memory.Reserve
	m, err := Compile(t.Context(), Spec{Patterns: []string{"needle"}, Dialect: Fixed}, limits)
	testutil.FailErr(t, "compile admitted matcher", err)
	return m
}

func scanMemoryFixture(t *testing.T, maximum int64) *toolcontract.ProgramMemory {
	t.Helper()
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: maximum, State: 1})
	testutil.FailErr(t, "create scan admission", err)
	return memory
}

type endlessScanInput struct{ read int }

func (r *endlessScanInput) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	r.read += len(p)
	return len(p), nil
}

func TestScanReaderRefusesOversizedRecordsBeforeGathering(t *testing.T) {
	memory := scanMemoryFixture(t, 1<<20)
	matcher := admittedMatcher(t, memory, 32)
	input := &endlessScanInput{}
	_, err := ScanReader(t.Context(), input, 0, nil, matcher, ScanOptions{MaxSelected: -1}, func(Line) error { return nil })
	if budgetResource(t, err) != "regex_input_bytes" || input.read > scanBufferBytes || memory.Peak().Working > scanBufferBytes || memory.InUse().Working != 0 {
		t.Fatalf("oversized record: err=%v read=%d peak=%+v held=%+v", err, input.read, memory.Peak(), memory.InUse())
	}
}

func TestScanReaderRecordBoundaryUsesLogicalText(t *testing.T) {
	for _, c := range []struct {
		input  string
		opts   ScanOptions
		refuse bool
	}{
		{"aaaa\n", ScanOptions{}, false},
		{"aaaa", ScanOptions{}, false},
		{"aaaa\r\n", ScanOptions{TrimCR: true}, false},
		{"aaaa\x00", ScanOptions{NullData: true}, false},
		{"aaaaa\n", ScanOptions{}, true},
		{"aaaa\r\n", ScanOptions{}, true},
		{"aaaaa\r\n", ScanOptions{TrimCR: true}, true},
	} {
		t.Run(c.input, func(t *testing.T) {
			memory := scanMemoryFixture(t, 1<<20)
			m := admittedMatcher(t, memory, 4)
			c.opts.MaxSelected = -1
			_, err := ScanReader(t.Context(), strings.NewReader(c.input), 0, nil, m, c.opts, func(Line) error { return nil })
			if c.refuse {
				if budgetResource(t, err) != "regex_input_bytes" {
					t.Fatalf("record limit: %v", err)
				}
			} else {
				testutil.FailErr(t, "scan boundary record", err)
			}
			if memory.InUse().Working != 0 {
				t.Fatalf("record memory leaked: %+v", memory.InUse())
			}
		})
	}
}

func TestScanReaderGrowthAccountsOldAndNewCapacity(t *testing.T) {
	for _, maximum := range []int64{2 * scanBufferBytes, 1 << 20} {
		memory := scanMemoryFixture(t, maximum)
		m := admittedMatcher(t, memory, 4*scanBufferBytes)
		text := strings.Repeat("a", 2*scanBufferBytes+7)
		_, err := ScanReader(t.Context(), strings.NewReader(text), 0, nil, m, ScanOptions{MaxSelected: -1}, func(Line) error { return nil })
		if maximum == 2*scanBufferBytes {
			var budget *toolcontract.ProgramMemoryLimitError
			if !errors.As(err, &budget) {
				t.Fatalf("growth admission: %v", err)
			}
		} else {
			testutil.FailErr(t, "scan grown record", err)
			if memory.Peak().Working < 4*scanBufferBytes {
				t.Fatalf("growth overlap not charged: %+v", memory.Peak())
			}
		}
		if memory.InUse().Working != 0 || memory.Peak().Working > maximum {
			t.Fatalf("growth memory: peak=%+v held=%+v", memory.Peak(), memory.InUse())
		}
	}
}

func TestScanReaderContextRetentionIsIncrementalAndBounded(t *testing.T) {
	memory := scanMemoryFixture(t, 1<<20)
	m := admittedMatcher(t, memory, 1024)
	var lines []int
	_, err := ScanReader(t.Context(), strings.NewReader(strings.Repeat("none\n", 100)+"needle\n"), 0, nil, m, ScanOptions{MaxSelected: -1, Before: 2}, func(line Line) error {
		lines = append(lines, line.Number)
		return nil
	})
	testutil.FailErr(t, "scan retained context", err)
	if len(lines) != 3 || lines[0] != 99 || lines[1] != 100 || lines[2] != 101 || memory.Peak().Working > scanBufferBytes+2*contextLineBytes+16 || memory.InUse().Working != 0 {
		t.Fatalf("context retention: lines=%v peak=%+v held=%+v", lines, memory.Peak(), memory.InUse())
	}
	_, err = ScanReader(t.Context(), strings.NewReader("one\ntwo\nneedle\n"), 0, nil, m, ScanOptions{MaxSelected: -1, Before: 1 << 30}, func(Line) error { return nil })
	testutil.FailErr(t, "incremental context capacity", err)
	if memory.Peak().Working > scanBufferBytes+8*contextLineBytes+32 || memory.InUse().Working != 0 {
		t.Fatalf("context count prepaid unused capacity: peak=%+v held=%+v", memory.Peak(), memory.InUse())
	}
}

type failedScanInput struct{ err error }

func (r failedScanInput) Read([]byte) (int, error) { return 0, r.err }

func TestScanReaderReleasesAdmissionOnEveryExit(t *testing.T) {
	failure := errors.New("fixture stop")
	for _, exit := range []string{"reader", "yield", "cancel", "context capacity"} {
		t.Run(exit, func(t *testing.T) {
			maximum := int64(1 << 20)
			if exit == "context capacity" {
				maximum = scanBufferBytes + 8*contextLineBytes
			}
			memory := scanMemoryFixture(t, maximum)
			m := admittedMatcher(t, memory, 1024)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var input io.Reader = strings.NewReader("none\nneedle\n")
			yield := func(Line) error { return failure }
			if exit == "reader" {
				input = failedScanInput{failure}
			}
			if exit == "cancel" {
				cancel()
			}
			_, err := ScanReader(ctx, input, 0, nil, m, ScanOptions{MaxSelected: -1, Before: 8}, yield)
			if err == nil || memory.InUse().Working != 0 {
				t.Fatalf("%s: err=%v held=%+v", exit, err, memory.InUse())
			}
		})
	}
}
