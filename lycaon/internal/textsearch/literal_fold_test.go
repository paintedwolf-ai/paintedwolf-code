package textsearch

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func forceMatcherVM(t *testing.T, matcher *Matcher) *Matcher {
	t.Helper()
	copy := *matcher
	engine, ok := copy.engine.(vmEngine)
	if !ok {
		t.Fatalf("compiled engine = %T", copy.engine)
	}
	engine.literal = nil
	copy.engine = engine
	return &copy
}

func compareFoldedMatchers(t *testing.T, fast, reference *Matcher, input []byte) {
	t.Helper()
	span, selected, err := fast.Select(t.Context(), input)
	wantSpan, wantSelected, wantErr := reference.Select(t.Context(), input)
	if selected != wantSelected || selected && span != wantSpan || err != nil || wantErr != nil {
		t.Fatalf("%q selection = %+v/%v/%v, VM %+v/%v/%v", input, span, selected, err, wantSpan, wantSelected, wantErr)
	}
	var spans, wantSpans []Span
	testutil.FailErr(t, "literal matches", fast.Each(t.Context(), input, func(span Span) error { spans = append(spans, span); return nil }))
	testutil.FailErr(t, "VM matches", reference.Each(t.Context(), input, func(span Span) error { wantSpans = append(wantSpans, span); return nil }))
	var groups, wantGroups [][]int
	testutil.FailErr(t, "literal groups", fast.Groups(t.Context(), input, func(group []int) error { groups = append(groups, append([]int(nil), group...)); return nil }))
	testutil.FailErr(t, "VM groups", reference.Groups(t.Context(), input, func(group []int) error { wantGroups = append(wantGroups, append([]int(nil), group...)); return nil }))
	if !reflect.DeepEqual(spans, wantSpans) || !reflect.DeepEqual(groups, wantGroups) || fast.CaptureGroups() != reference.CaptureGroups() {
		t.Fatalf("%q matches=%v VM=%v groups=%v VM=%v", input, spans, wantSpans, groups, wantGroups)
	}
}

func TestFoldedLiteralMatchesCompiledVMAcrossDialects(t *testing.T) {
	inputs := []string{"", "MLX\n", "a\n", "a\n\n", "M\nLX", "MLX", "mLx mlx MLX", "aB abC", "a.b [key] x*y", "K k K", "S s ſ", "I i ı İ", "Σ σ ς", "ß ẞ", "�", "\x00a\x00", "mlx\xffMLX", "\xff�mlx"}
	patterns := [][]string{{"mlx"}, {"a", "ab"}, {"ab", "a"}, {"", "a"}, {""}, {"K"}, {"s"}, {"i"}, {"Σ"}, {"ß"}, {"İ"}, {"�"}, {"a.b"}, {"[key]"}, {"x*y"}, {"\x00"}, {"(ab)"}}
	for _, dialect := range []Dialect{Basic, Extended, Fixed, RE2, Perl} {
		for _, pattern := range patterns {
			for _, mode := range []struct{ line, word bool }{{}, {line: true}, {word: true}} {
				t.Run(fmt.Sprint(dialect, pattern, mode), func(t *testing.T) {
					m, err := Compile(t.Context(), Spec{Dialect: dialect, Patterns: pattern, IgnoreCase: true, Line: mode.line, Word: mode.word}, catalogLimits(t))
					testutil.FailErr(t, "compile folded matcher", err)
					for _, input := range inputs {
						compareFoldedMatchers(t, m, forceMatcherVM(t, m), []byte(input))
					}
				})
			}
		}
	}
}

func TestFoldedLiteralUsesNoRecordSessionForASCIICaseCombinations(t *testing.T) {
	memory := scanMemoryFixture(t, 1<<20)
	limits := catalogLimits(t)
	limits.Reserve = memory.Reserve
	m, err := Compile(t.Context(), Spec{Dialect: Basic, Patterns: []string{"mlx"}, IgnoreCase: true}, limits)
	testutil.FailErr(t, "compile folded literal", err)
	if m.engine.(vmEngine).literal == nil {
		t.Fatal("pure folded literal did not select the literal owner")
	}
	before := memory.Reservations()
	for mask := range 8 {
		input := []byte("mlx")
		for i := range input {
			if mask&(1<<i) != 0 {
				input[i] -= 'a' - 'A'
			}
		}
		_, selected, err := m.Select(t.Context(), input)
		testutil.FailErr(t, "select ASCII case combination", err)
		if !selected {
			t.Fatalf("case combination did not match: %q", input)
		}
	}
	if memory.Reservations() != before || memory.InUse().Working != 0 {
		t.Fatalf("literal execution borrowed VM memory: before=%d after=%d held=%+v", before, memory.Reservations(), memory.InUse())
	}
	_, selected, err := forceMatcherVM(t, m).Select(t.Context(), []byte("MLX"))
	testutil.FailErr(t, "select forced VM reference", err)
	if !selected || memory.Reservations() <= before || memory.InUse().Working != 0 {
		t.Fatalf("reference VM admission: selected=%v before=%d after=%d held=%+v", selected, before, memory.Reservations(), memory.InUse())
	}
}

type foldedChunkReader struct{ input io.Reader }

func (r foldedChunkReader) Read(p []byte) (int, error) { return r.input.Read(p[:min(1, len(p))]) }

func TestFoldedLiteralAndVMKeepStreamContextAndByteOffsets(t *testing.T) {
	m, err := Compile(t.Context(), Spec{Dialect: Basic, Patterns: []string{"mlx"}, IgnoreCase: true}, catalogLimits(t))
	testutil.FailErr(t, "compile streamed folded matcher", err)
	const input = "prefix\nMLX\nK context\nmlx\xff\nlast\n"
	for _, opts := range []ScanOptions{{MaxSelected: -1}, {MaxSelected: 1, Before: 1, After: 1}, {MaxSelected: -1, Invert: true}, {MaxSelected: -1, NullData: true}} {
		var got, want []Line
		for i, matcher := range []*Matcher{m, forceMatcherVM(t, m)} {
			_, err := ScanReader(t.Context(), foldedChunkReader{strings.NewReader(input)}, 0, nil, matcher, opts, func(line Line) error {
				line.Text = append([]byte(nil), line.Text...)
				if i == 0 {
					got = append(got, line)
				} else {
					want = append(want, line)
				}
				return nil
			})
			testutil.FailErr(t, "scan folded stream", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("stream options %+v: got=%+v VM=%+v", opts, got, want)
		}
	}
}

func TestFoldedLiteralChecksWorkAndCancellation(t *testing.T) {
	m, err := Compile(t.Context(), Spec{Dialect: Basic, Patterns: []string{"aaaa"}, IgnoreCase: true}, catalogLimits(t))
	testutil.FailErr(t, "compile bounded folded matcher", err)
	engine := m.engine.(vmEngine)
	engine.limits.WorkSteps = 5
	m.engine = engine
	if _, _, err := m.Select(t.Context(), []byte("AAAA")); budgetResource(t, err) != "regex_work_steps" {
		t.Fatalf("folded comparison work = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := m.Select(ctx, []byte("AAAA")); err != context.Canceled {
		t.Fatalf("folded cancellation = %v", err)
	}
}
