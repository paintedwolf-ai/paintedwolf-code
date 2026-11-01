package textsearch

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRE2ValidatedVMKeepsCaptureAndScalarSemantics(t *testing.T) {
	for _, pattern := range []string{`(a|ab)`, `(a+?)(a*)`, `(?i)(k+)`, `(?m)^(a*)$`, `\b(a+)\b`, `(?:a?)*`, `(a{1,3}?)(a*)`, string(rune(0x10fff4)) + `(a?)`} {
		reference, err := regexp.Compile(pattern)
		testutil.FailErr(t, "regexp.Compile failed", err)
		matcher, err := Compile(t.Context(), Spec{Patterns: []string{pattern}, Dialect: RE2}, catalogLimits(t))
		testutil.FailErr(t, "Compile failed", err)
		for _, input := range []string{"", "ab aaa", "aa\na", "Kk K", "éaaaé", string(rune(0x10fff4)) + "a"} {
			want := reference.FindAllStringSubmatchIndex(input, -1)
			want = slices.DeleteFunc(want, func(g []int) bool { return g[0] == g[1] })
			var got [][]int
			err := matcher.Groups(t.Context(), []byte(input), func(g []int) error { got = append(got, slices.Clone(g)); return nil })
			if err != nil || !slices.EqualFunc(got, want, slices.Equal) {
				t.Fatalf("pattern %q input %q groups %v want %v err %v", pattern, input, got, want, err)
			}
		}
	}
	marker := string(rune(0x10fff4))
	for _, word := range []bool{false, true} {
		matcher, err := Compile(t.Context(), Spec{Patterns: []string{marker}, Dialect: RE2, Word: word}, catalogLimits(t))
		testutil.FailErr(t, "Compile failed", err)
		span, ok, err := matcher.Select(t.Context(), []byte(" "+marker+" "))
		if err != nil || !ok || span != (Span{1, 1 + len(marker)}) {
			t.Fatalf("word %v literal marker span %v %v %v", word, span, ok, err)
		}
	}
}

// The caller cancels after the session has admitted record memory. Every
// dialect uses the same VM check before further matching or allocation.
func TestRegexDialectsCancelAndReleaseSessionAdmission(t *testing.T) {
	for _, dialect := range []Dialect{Basic, Extended, RE2, Perl} {
		matcher, err := Compile(t.Context(), Spec{Patterns: []string{`a+`}, Dialect: dialect}, catalogLimits(t))
		testutil.FailErr(t, "Compile failed", err)
		program := matcher.engine.(vmEngine).program
		ctx, cancel := context.WithCancel(t.Context())
		var held int64
		limits := catalogLimits(t).syntaxBudget()
		calls := 0
		limits.Reserve = func(_ context.Context, n int64) (func(), error) {
			held += n
			calls++
			if calls > 1 {
				cancel()
			}
			return func() { held -= n }, nil
		}
		session, err := program.Session(ctx, limits)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if err = session.ReserveMemory(1024); err != nil {
			testutil.FailErr(t, "session.ReserveMemory failed", err)
		}
		_, err = session.Find([]rune("aaaa"), 0, false)
		session.Close()
		cancel()
		if !errors.Is(err, context.Canceled) || held != 0 || calls < 2 {
			t.Fatalf("%s err %v held %d calls %d", dialect, err, held, calls)
		}
	}
}

func TestRecordDecodeAdmissionPrecedesAllocation(t *testing.T) {
	matcher, err := Compile(t.Context(), Spec{Patterns: []string{"a"}, Dialect: RE2}, catalogLimits(t))
	testutil.FailErr(t, "Compile failed", err)
	vm := matcher.engine.(vmEngine)
	vm.limits.MemoryBytes = 1024
	_, _, err = vm.first(t.Context(), make([]byte, 1024))
	if budgetResource(t, err) != "evaluation_memory_bytes" {
		t.Fatal(err)
	}
}
