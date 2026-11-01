package textsearch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type bufferScanResult struct {
	lines    []Line
	selected int
	err      string
}

func bufferScan(t *testing.T, m *Matcher, input string, opts ScanOptions, maximum int64, baseline bool) bufferScanResult {
	t.Helper()
	if baseline && opts.Before == 0 {
		opts.Before = -1
	}
	var result bufferScanResult
	count, err := ScanReader(t.Context(), strings.NewReader(input), maximum, nil, m, opts, func(line Line) error {
		line.Text = append([]byte(nil), line.Text...)
		result.lines = append(result.lines, line)
		return nil
	})
	result.selected = count
	if err != nil {
		result.err = err.Error()
	}
	return result
}

func TestBufferedRejectionMatchesRecordScanning(t *testing.T) {
	inputs := []string{
		"", "\n\n", strings.Repeat("irrelevant\n", 12000) + "MLX needle\nlast",
		strings.Repeat("a\n", 40000) + "needle\r\n\x00tail",
		"K ſ\nmlx\xff\nneedle\n", strings.Repeat("a", scanBufferBytes+3) + "\nneedle",
		"a\x00b\x00needle\x00", "a\nneedle\r\nlast\r", "nee\ndle\n",
	}
	specs := []Spec{
		{Dialect: Fixed, Patterns: []string{"needle"}},
		{Dialect: Basic, Patterns: []string{"needle", "mlx"}, IgnoreCase: true},
		{Dialect: Basic, Patterns: []string{"K"}, IgnoreCase: true},
		{Dialect: Basic, Patterns: []string{"needle"}, Line: true},
		{Dialect: Fixed, Patterns: []string{""}},
		{Dialect: Fixed, Patterns: []string{"a\xff"}},
		{Dialect: Extended, Patterns: []string{"n.*e"}},
	}
	for _, spec := range specs {
		m, err := Compile(t.Context(), spec, catalogLimits(t))
		testutil.FailErr(t, "compile buffered differential", err)
		for i, input := range inputs {
			for _, opts := range []ScanOptions{{MaxSelected: -1}, {MaxSelected: 1}, {MaxSelected: -1, TrimCR: true}, {MaxSelected: -1, NullData: true}, {MaxSelected: -1, Invert: true}, {MaxSelected: 1, Before: 1, After: 1}} {
				got, want := bufferScan(t, m, input, opts, 0, false), bufferScan(t, m, input, opts, 0, true)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%+v input %d opts %+v: got %+v want %+v", spec, i, opts, got, want)
				}
			}
		}
	}
}

func TestBufferedRejectionPreservesBudgetRefusals(t *testing.T) {
	for _, dialect := range []Dialect{Fixed, Basic} {
		for _, work := range []int64{3, 40, 1 << 30} {
			for _, inputLimit := range []int64{4, 1 << 20} {
				limits := catalogLimits(t)
				m, err := Compile(t.Context(), Spec{Dialect: dialect, Patterns: []string{"needle"}}, limits)
				testutil.FailErr(t, "compile budget differential", err)
				limits.WorkSteps, limits.InputBytes = work, inputLimit
				m.limits = limits
				switch e := m.engine.(type) {
				case literalEngine:
					e.limits = limits
					m.engine = e
				case vmEngine:
					e.limits = limits
					m.engine = e
				}
				for _, maximum := range []int64{0, 7, scanBufferBytes + 7} {
					input := strings.Repeat("a\n", 40000) + "needle\n"
					opts := ScanOptions{MaxSelected: -1}
					got, want := bufferScan(t, m, input, opts, maximum, false), bufferScan(t, m, input, opts, maximum, true)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("%s work %d input %d max %d: got %+v want %+v", dialect, work, inputLimit, maximum, got, want)
					}
				}
			}
		}
	}
}

func TestBufferedRejectionConsumesOnlyCompleteProvenRecords(t *testing.T) {
	m, err := Compile(t.Context(), Spec{Dialect: Fixed, Patterns: []string{"needle"}}, catalogLimits(t))
	testutil.FailErr(t, "compile rejection proof", err)
	memory := scanMemoryFixture(t, scanBufferBytes)
	m.limits.Reserve = memory.Reserve
	opts := ScanOptions{MaxSelected: -1}
	input, err := newScanRecordReader(t.Context(), strings.NewReader("a\nb\nnee"), m, opts)
	testutil.FailErr(t, "open bounded reader", err)
	reject := newBufferRejector(m, opts)
	skipped, records := reject.skip(t.Context(), input, '\n', 0, 0)
	if skipped != 4 || records != 2 {
		t.Fatalf("rejection = %d bytes %d records", skipped, records)
	}
	raw, err := input.read('\n', 4, 0, nil)
	if string(raw) != "nee" || !errors.Is(err, io.EOF) {
		t.Fatalf("partial record %q: %v", raw, err)
	}
	input.close()
	if memory.InUse().Working != 0 || memory.Peak().Working != scanBufferBytes {
		t.Fatalf("buffer memory held=%+v peak=%+v", memory.InUse(), memory.Peak())
	}
}

func TestBufferedRejectionPreservesReaderAndYieldErrors(t *testing.T) {
	m, err := Compile(t.Context(), Spec{Dialect: Fixed, Patterns: []string{"needle"}}, catalogLimits(t))
	testutil.FailErr(t, "compile error fixture", err)
	sentinel := errors.New("fixture read failure")
	input := io.MultiReader(strings.NewReader(strings.Repeat("a\n", 40000)), errorBufferReader{sentinel})
	_, err = ScanReader(t.Context(), input, 0, nil, m, ScanOptions{MaxSelected: -1}, func(Line) error { return nil })
	if !errors.Is(err, sentinel) {
		t.Fatalf("read error: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	_, err = ScanReader(ctx, strings.NewReader("needle\n"+strings.Repeat("a\n", 40000)), 0, nil, m, ScanOptions{MaxSelected: -1}, func(Line) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("yield cancellation: %v", err)
	}
	_, err = ScanReader(t.Context(), strings.NewReader("a\nneedle\n"), 0, nil, m, ScanOptions{MaxSelected: -1}, func(Line) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("yield error: %v", err)
	}
}

type errorBufferReader struct{ err error }

func (r errorBufferReader) Read([]byte) (int, error) { return 0, r.err }

func BenchmarkBufferedLiteralScan(b *testing.B) {
	for _, fold := range []bool{false, true} {
		for _, baseline := range []bool{false, true} {
			b.Run(fmt.Sprintf("fold=%v/records=%v", fold, baseline), func(b *testing.B) {
				limits, err := CatalogLimits()
				if err != nil {
					b.Fatal(err)
				}
				m, err := Compile(context.Background(), Spec{Dialect: Basic, Patterns: []string{"needle", "target"}, IgnoreCase: fold}, limits)
				if err != nil {
					b.Fatal(err)
				}
				input := strings.Repeat("ordinary irrelevant line\n", 12000) + "needle\n"
				opts := ScanOptions{MaxSelected: 1}
				if baseline {
					opts.Before = -1
				}
				b.SetBytes(int64(len(input)))
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					_, err := ScanReader(context.Background(), strings.NewReader(input), 0, nil, m, opts, func(Line) error { return nil })
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func TestBufferedCandidatesAreExaminedOncePerBuffer(t *testing.T) {
	m, err := Compile(t.Context(), Spec{Dialect: Fixed, Patterns: []string{"needle"}}, catalogLimits(t))
	testutil.FailErr(t, "compile candidate fixture", err)
	opts := ScanOptions{MaxSelected: -1}
	input, err := newScanRecordReader(t.Context(), strings.NewReader("needle\nmiss\n"), m, opts)
	testutil.FailErr(t, "open candidate reader", err)
	defer input.close()
	reject := newBufferRejector(m, opts)
	if skipped, _ := reject.skip(t.Context(), input, '\n', 0, 0); skipped != 0 || reject.remaining != 12 {
		t.Fatalf("candidate buffer skipped=%d remaining=%d", skipped, reject.remaining)
	}
	raw, err := input.read('\n', 0, 0, nil)
	testutil.FailErr(t, "read candidate", err)
	reject.consumed(len(raw))
	if skipped, _ := reject.skip(t.Context(), input, '\n', int64(len(raw)), 0); skipped != 0 || reject.remaining != 5 {
		t.Fatalf("candidate buffer rescanned: skipped=%d remaining=%d", skipped, reject.remaining)
	}
}
