package textsearch

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPOSIXEngineFindsPositionedLongestMatchesWithSubexpressions(t *testing.T) {
	engine, err := NewPOSIXEngine()
	testutil.FailErr(t, "create POSIX regex engine", err)
	for _, c := range []struct {
		name     string
		pattern  string
		extended bool
		subject  string
		from     int
		want     []int
		groups   int
	}{
		{"subexpression rule prefers the earlier group longest", `(a|ab)(c|bcd)(d*)`, true, "abcd", 0, []int{0, 4, 0, 2, 2, 3, 3, 4}, 3},
		{"first group takes the longest share", `(a*)(a*)`, true, "aa", 0, []int{0, 2, 0, 2, 2, 2}, 2},
		{"alternation inside groups", `(ab|a)(bc|c)`, true, "abc", 0, []int{0, 3, 0, 2, 2, 3}, 2},
		{"BRE back reference", `\(a*\)\(b\)\1`, false, "xaabaa", 0, []int{1, 6, 1, 3, 3, 4}, 2},
		{"last iteration of a repeated group", `\(a\|b\)*`, false, "ab", 0, []int{0, 2, 1, 2}, 1},
		{"unset group", `(a)|b`, true, "b", 0, []int{0, 1, -1, -1}, 1},
		{"empty match is reported", `x*`, false, "abc", 0, []int{0, 0}, 0},
		{"empty match at the offset", `x*`, false, "abc", 2, []int{2, 2}, 0},
		{"positioned search", `ab`, false, "ab ab", 1, []int{3, 5}, 0},
		{"anchor sees the whole subject", `^ab`, false, "ab ab", 1, nil, 0},
		{"word start sees the previous byte", `\<b`, false, "ab b", 1, []int{3, 4}, 0},
		{"newline escape", `a\nb`, false, "a\nb", 0, []int{0, 3}, 0},
		{"dot matches newline", `a.b`, false, "a\nb", 0, []int{0, 3}, 0},
		{"negated class matches newline", `a[^x]b`, true, "a\nb", 0, []int{0, 3}, 0},
		{"byte offsets across scalars", `é+`, true, "aéé", 1, []int{1, 5}, 0},
		{"offset beyond the subject", `a`, false, "a", 2, nil, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			re, err := engine.Compile(t.Context(), c.pattern, c.extended)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			got, err := re.Find(t.Context(), []byte(c.subject), c.from)
			if err != nil || !slices.Equal(got, c.want) || re.Groups() != c.groups {
				t.Fatalf("find %q in %q from %d = %v (%d groups), %v; want %v (%d)", c.pattern, c.subject, c.from, got, re.Groups(), err, c.want, c.groups)
			}
		})
	}
}

func TestPOSIXEngineFoldsCaseOnRequest(t *testing.T) {
	engine, err := NewPOSIXEngine()
	testutil.FailErr(t, "create POSIX regex engine", err)
	folded, err := engine.CompileFolded(t.Context(), `a[b-c]\(É\)`, false)
	testutil.FailErr(t, "compile case-folded POSIX pattern", err)
	if got, err := folded.Find(t.Context(), []byte("xACé"), 0); err != nil || !slices.Equal(got, []int{1, 5, 3, 5}) {
		t.Fatalf("folded find = %v, %v", got, err)
	}
	exact, err := engine.Compile(t.Context(), `a[b-c]`, false)
	testutil.FailErr(t, "compile POSIX pattern", err)
	if got, err := exact.Find(t.Context(), []byte("AC"), 0); err != nil || got != nil {
		t.Fatalf("exact find = %v, %v", got, err)
	}
}

// TestPOSIXEngineSubstitutesGlobally drives the engine the way s///g does:
// an empty match right after a previous match is skipped and the search
// advances one scalar.
func TestPOSIXEngineSubstitutesGlobally(t *testing.T) {
	engine, err := NewPOSIXEngine()
	testutil.FailErr(t, "create POSIX regex engine", err)
	re, err := engine.Compile(t.Context(), `x*`, false)
	testutil.FailErr(t, "compile POSIX pattern", err)
	subject := []byte("abxc")
	var out strings.Builder
	copied, previous := 0, -1
	for from := 0; from <= len(subject); {
		m, err := re.Find(t.Context(), subject, from)
		testutil.FailErr(t, "find next POSIX match", err)
		if m == nil {
			break
		}
		empty := m[0] == m[1]
		if empty && m[0] == previous {
			from = m[0] + 1
			continue
		}
		out.Write(subject[copied:m[0]])
		out.WriteString("-")
		copied, previous = m[1], m[1]
		from = m[1]
		if m[0] == m[1] {
			from++
		}
	}
	out.Write(subject[copied:])
	if got := out.String(); got != "-a-b-c-" {
		t.Fatalf("s/x*/-/g = %q", got)
	}
}

func TestPOSIXEngineRefusesAndMeters(t *testing.T) {
	engine, err := NewPOSIXEngine()
	testutil.FailErr(t, "create POSIX regex engine", err)
	var pattern *PatternError
	if _, err := engine.Compile(t.Context(), `\(`, false); !errors.As(err, &pattern) || pattern.Code != CodeRegexInvalid {
		t.Fatalf("invalid: %v", err)
	}
	if _, err := engine.Compile(t.Context(), `\d`, false); !errors.As(err, &pattern) || pattern.Code != CodeRegexUnsupported {
		t.Fatalf("unsupported: %v", err)
	}
	if _, err := engine.Compile(t.Context(), strings.Repeat("a", int(engine.Limits.PatternBytes)+1), true); budgetResource(t, err) != "regex_pattern_bytes" {
		t.Fatalf("pattern bytes: %v", err)
	}
	re, err := engine.Compile(t.Context(), `\(a*\)*\1b`, false)
	testutil.FailErr(t, "engine.Compile failed", err)
	if _, err := re.Find(t.Context(), []byte(strings.Repeat("a", 40)), 0); budgetResource(t, err) != "regex_work_steps" {
		t.Fatalf("catastrophic: %v", err)
	}
	small := engine
	small.Limits.InputBytes = 2
	re, err = small.Compile(t.Context(), `a`, false)
	testutil.FailErr(t, "compile input-bounded POSIX pattern", err)
	if _, err := re.Find(t.Context(), []byte("aaa"), 0); budgetResource(t, err) != "regex_input_bytes" {
		t.Fatalf("input bytes: %v", err)
	}
}
