package argv

import (
	"fmt"
	"reflect"
	"testing"
	"unicode"
)

// FuzzJoinCommandLineRoundTrips checks accepted argv round trips.
func FuzzJoinCommandLineRoundTrips(f *testing.F) {
	seeds := []string{
		``,
		`ls`,
		`grep "a\|b" x`,
		`python3 -c "import x; x.run()"`,
		`a "" b`,
		`"\\"`,
		"rm x ",
		"echo a b",
		"cmd \v",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		env, name, args, err := SplitCommandLine(line)
		if err != nil {
			return
		}
		joined := JoinCommandLine(env, name, args)
		env2, name2, args2, err := SplitCommandLine(joined)
		if err != nil {
			t.Fatalf("joined form no longer parses\n  input:  %q\n  joined: %q\n  err:    %v", line, joined, err)
		}
		if (len(env) > 0 || len(env2) > 0) && !reflect.DeepEqual(env, env2) {
			t.Fatalf("round trip changed env\n  input:  %q\n  joined: %q\n  before: %v\n  after:  %v", line, joined, env, env2)
		}
		if name2 != name {
			t.Fatalf("round trip changed the program\n  input:  %q\n  joined: %q\n  before: %q\n  after:  %q", line, joined, name, name2)
		}
		if len(args2) != len(args) {
			t.Fatalf("round trip changed argv length\n  input:  %q\n  joined: %q\n  before: %q\n  after:  %q", line, joined, args, args2)
		}
		for i := range args {
			if args[i] != args2[i] {
				t.Fatalf("round trip changed arg %d\n  input:  %q\n  joined: %q\n  before: %q\n  after:  %q", i, line, joined, args[i], args2[i])
			}
		}
	})
}

// TestJoinCommandLineQuotesEveryUnicodeSpace checks the full rune class.
func TestJoinCommandLineQuotesEveryUnicodeSpace(t *testing.T) {
	t.Parallel()
	var broken []string
	checked := 0
	for r := rune(1); r < unicode.MaxRune; r++ {
		if !unicode.IsSpace(r) {
			continue
		}
		checked++
		for _, shape := range []struct {
			label string
			arg   string
		}{
			{"leading", string(r) + "x"},
			{"trailing", "x" + string(r)},
			{"interior", "a" + string(r) + "b"},
			{"alone", string(r)},
		} {
			line := JoinCommandLine(nil, "cmd", []string{shape.arg})
			_, name, args, err := SplitCommandLine(line)
			switch {
			case err != nil:
				broken = append(broken, fmt.Sprintf("U+%04X %-8s joined %q no longer parses: %v", r, shape.label, line, err))
			case name != "cmd" || len(args) != 1 || args[0] != shape.arg:
				broken = append(broken, fmt.Sprintf("U+%04X %-8s joined %q reparsed as %q %q, want one arg %q", r, shape.label, line, name, args, shape.arg))
			}
		}
	}
	if checked == 0 {
		t.Fatal("no space runes examined; the sweep is not testing anything")
	}
	for _, b := range broken {
		t.Error(b)
	}
}

// TestArgSeparatorKeepsNonBreakingSpaceAsData checks interior spacing.
func TestArgSeparatorKeepsNonBreakingSpaceAsData(t *testing.T) {
	t.Parallel()
	// Escape the non-breaking space so its identity stays visible.
	const nbsp = "\u00a0"
	_, name, args, err := SplitCommandLine("rm a" + nbsp + "b")
	if err != nil {
		t.Fatalf("SplitCommandLine returned %v, want a successful parse", err)
	}
	if want := "a" + nbsp + "b"; name != "rm" || len(args) != 1 || args[0] != want {
		t.Fatalf("got %q %q, want rm with one argument %q", name, args, want)
	}
}

// TestTrimmedEdgeIsWiderThanArgSeparator checks the quoting relation.
func TestTrimmedEdgeIsWiderThanArgSeparator(t *testing.T) {
	t.Parallel()
	for r := rune(1); r < unicode.MaxRune; r++ {
		if argSeparator(r) && !trimmedEdge(r) {
			t.Fatalf("U+%04X separates argv but is not trimmed at a line edge; quoting derives from both", r)
		}
	}
}
