package exposure

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

func TestFoundAgreesWithSubstringSearch(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	text := func(n int) string {
		var b strings.Builder
		for range n {
			b.WriteByte("abc"[rng.IntN(3)])
		}
		return b.String()
	}
	for range 2000 {
		values := make([]string, 1+rng.IntN(6))
		for i := range values {
			values[i] = text(1 + rng.IntN(5))
		}
		output := text(rng.IntN(40))
		scanner := NewScanner(values...)
		var want []string
		for _, v := range scanner.values {
			if strings.Contains(output, v) {
				want = append(want, v)
			}
		}
		if got := scanner.Found(output); !slices.Equal(got, want) {
			t.Fatalf("Found(%q) with %q = %q, want %q", output, values, got, want)
		}
	}
}

func TestFoundHandlesNestedValuesAndNoValues(t *testing.T) {
	if got := NewScanner("abcd", "bc", "cd", "").Found("xabcdx"); !slices.Equal(got, []string{"abcd", "bc", "cd"}) {
		t.Fatalf("nested values = %q", got)
	}
	if got := NewScanner().Found("anything"); got != nil {
		t.Fatalf("no values found %q", got)
	}
	if got := NewScanner("héllo").Found("say héllo"); len(got) != 1 {
		t.Fatalf("multi-byte value = %q", got)
	}
}

func TestCleanScanDoesNotAllocate(t *testing.T) {
	scanner := NewScanner("first-sentinel", "second-sentinel", "third-sentinel")
	output := strings.Repeat("ordinary output without any of them ", 1000)
	if allocs := testing.AllocsPerRun(20, func() {
		if scanner.Found(output) != nil {
			t.Fatal("clean output matched")
		}
	}); allocs != 0 {
		t.Fatalf("clean scan allocated %v times", allocs)
	}
}

type recordingTB struct {
	testing.TB
	failed bool
}

func (r *recordingTB) Helper()               {}
func (r *recordingTB) Fatalf(string, ...any) { r.failed = true }

func TestAbsentAndPresentReportTheirValues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func(testing.TB)
		fails bool
	}{
		{"absent clean", func(tb testing.TB) { Absent(tb, "clean", "secret") }, false},
		{"absent exposed", func(tb testing.TB) { Absent(tb, "a secret here", "secret") }, true},
		{"present all", func(tb testing.TB) { Present(tb, "one two", "one", "two") }, false},
		{"present partial", func(tb testing.TB) { Present(tb, "one", "one", "two") }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingTB{TB: t}
			tc.check(rec)
			if rec.failed != tc.fails {
				t.Fatalf("failed = %v, want %v", rec.failed, tc.fails)
			}
		})
	}
}

func TestValuesReadsNestedInput(t *testing.T) {
	type inner struct {
		Secret string
		Raw    []byte
	}
	shared := &inner{Secret: "pointer-value", Raw: []byte("byte-value")}
	input := map[string]any{
		"map-key": []any{"slice-value", shared, shared},
		"num":     7,
		"nil":     (*inner)(nil),
	}
	got := Values(input, "top-value", "abc")
	for _, want := range []string{"map-key", "slice-value", "pointer-value", "byte-value", "top-value"} {
		if !slices.Contains(got, want) {
			t.Errorf("Values missing %q in %q", want, got)
		}
	}
	if slices.Contains(got, "abc") {
		t.Error("Values kept a value too short to signal a leak")
	}
	cyclic := map[string]any{"name": "cycle-value"}
	cyclic["self"] = cyclic
	if got := Values(cyclic); !slices.Contains(got, "cycle-value") {
		t.Errorf("cyclic input = %q", got)
	}
}

func BenchmarkFound(b *testing.B) {
	values := make([]string, 200)
	for i := range values {
		values[i] = strings.Repeat("sentinel", 2) + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	scanner := NewScanner(values...)
	output := strings.Repeat("ordinary output without any of them ", 30000)
	b.SetBytes(int64(len(output)))
	for b.Loop() {
		if scanner.Found(output) != nil {
			b.Fatal("clean output matched")
		}
	}
}
