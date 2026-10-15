package toolcommand

import (
	"strings"
	"testing"
)

func TestFileWriteReplacementPreservesConsumerSemantics(t *testing.T) {
	cases := []struct {
		name      string
		command   string
		stdin     string
		wantExact bool
	}{
		{"cat identity control", "cat > out.txt", "xy", true},
		{"dd preserves existing suffix", "dd of=out.txt conv=notrunc", "xy", false},
		{"dd append opening flags", "dd of=out.txt oflag=append", "xy", false},
		{"dd invalid conversion", "dd of=out.txt conv=notrunc,append", "xy", false},
		{"wrapper changes directory", "env --chdir=/tmp sh -c 'cat > out.txt'", "xy", false},
		{"pipeline changes case", "cat | tr a-z A-Z > out.txt", "xy", false},
		{"pipeline selects first line", "cat | head -n 1 > out.txt", "first\nsecond\n", false},
		{"pipeline numbers lines", "cat | cat -n > out.txt", "xy\n", false},
		{"producer discards pipeline stream", "cat > /dev/null | cat > out.txt", "xy", false},
		{"producer appends to null before pipe", "cat >> /dev/null | cat > out.txt", "xy", false},
		{"last redirect discards stream", "cat > out.txt > /dev/null", "xy", false},
		{"empty command truncates only", "> out.txt", "xy", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, matched := InspectCommandFileWrite(t.Context(), "command", map[string]any{"command": tc.command, "stdin": tc.stdin}, t.TempDir())
			if (tc.wantExact && !matched) || got.Exact != tc.wantExact || (!tc.wantExact && len(got.Calls) != 0) {
				t.Fatalf("command=%q stdin=%q: matched=%t exact=%t replacement=%#v; want exact=%t without semantic changes", tc.command, tc.stdin, matched, got.Exact, got.Calls, tc.wantExact)
			}
		})
	}
}

func TestLiteralOutputFileWriteParity(t *testing.T) {
	cases := []struct {
		command                    string
		matched, exact, appendMode bool
		content                    string
	}{
		{`echo built > out.log`, true, true, false, "built\n"},
		{`echo built | cat > out.log`, true, true, false, "built\n"},
		{`echo built | tr a-z A-Z > out.log`, true, false, false, ""},
		{`echo 'two words' >> 'out file.txt'`, true, true, true, "two words\n"},
		{`printf '%s\n' 'hello' > out.txt`, true, true, false, "hello\n"},
		{`echo 'a\nb' > out.txt`, true, false, false, ""},
		{`echo -e hello > out.txt`, true, false, false, ""},
		{`echo -E hello > out.txt`, true, false, false, ""},
		{`echo -n hello > out.txt`, true, false, false, ""},
		{`echo "$VALUE" > out.txt`, true, false, false, ""},
		{`printf '%d' 12 > out.txt`, true, false, false, ""},
		{`echo hello`, false, false, false, ""},
		{`echo hello > /dev/null`, false, false, false, ""},
		{`echo hello > out.txt > /dev/null`, false, false, false, ""},
		{`echo hello > one.txt > two.txt`, false, false, false, ""},
		{`ls > listing.txt`, false, false, false, ""},
		{`cat source.txt > copy.txt`, false, false, false, ""},
	}
	for _, tc := range cases {
		for _, wrapped := range []bool{false, true} {
			command := tc.command
			if wrapped {
				command = "sh -c '" + strings.ReplaceAll(command, "'", "'\"'\"'") + "'"
			}
			t.Run(command, func(t *testing.T) {
				got, matched := InspectCommandFileWrite(t.Context(), "command", map[string]any{"command": command}, t.TempDir())
				if matched != tc.matched || got.Exact != tc.exact {
					t.Fatalf("matched=%t exact=%t, want %t/%t: %#v", matched, got.Exact, tc.matched, tc.exact, got)
				}
				if !tc.exact {
					if len(got.Calls) != 0 {
						t.Fatalf("uncertain output has replacements: %#v", got.Calls)
					}
					return
				}
				if got.Append != tc.appendMode || len(got.Calls) != 1 || got.Calls[0].Args["content"] != tc.content {
					t.Fatalf("replacement changed output: %#v", got)
				}
			})
		}
	}
}

func TestLiteralOutputDirectArgvMustAgree(t *testing.T) {
	for _, command := range []string{`printf '%s' a\qb > out.txt`, `printf '%s' @scratch/body > out.txt`, `printf '%s' body > out\q.txt`} {
		got, matched := InspectCommandFileWrite(t.Context(), "command", map[string]any{"command": command}, t.TempDir())
		if !matched || got.Exact || len(got.Calls) != 0 {
			t.Fatalf("direct argv disagreement must retain inexact recovery: %#v, matched=%t", got, matched)
		}
	}
}
