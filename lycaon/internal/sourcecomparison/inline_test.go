package sourcecomparison

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestInlineChangesWholeWords(t *testing.T) {
	old, next := inlineChanges("const originalName = value;\n", "const replacementName = value;\n")
	if !reflect.DeepEqual(old, []api.SourceReaderSpan{{From: 6, To: 18}}) || !reflect.DeepEqual(next, []api.SourceReaderSpan{{From: 6, To: 21}}) {
		t.Fatalf("word changes = %v / %v", old, next)
	}
}

func TestInlineChangesUnrelatedProse(t *testing.T) {
	a, b := "Read applicable nested policy before backend work.", "Keep verification receipts available during review."
	old, next := inlineChanges(a, b)
	if len(old) != 1 || old[0].From != 0 || old[0].To < width(a)-1 || len(next) != 1 || next[0].To < width(b)-1 {
		t.Fatalf("unrelated prose fragmented: %v / %v", old, next)
	}
}

func TestInlineChangesAcrossRewrappedLines(t *testing.T) {
	document, err := New(api.SourceComparisonSide{Content: "Read applicable nested policy\nbefore backend work.\n"}, api.SourceComparisonSide{Content: "Read applicable\nnested policy before backend work.\n"}, nil)
	testutil.FailErr(t, "prepare rewrapped paragraph", err)
	rows := frameForTest(t, document, 0, 20, "full").Rows
	for _, row := range rows {
		for _, span := range row.Changed {
			if strings.TrimSpace(row.Text[span.From:span.To]) != "" {
				t.Fatalf("rewrap marked words: %+v", row)
			}
		}
	}
}

func TestInlineChangesUnicodeCoordinates(t *testing.T) {
	old, next := inlineChanges("🙂 café old\n", "🙂 café new\n")
	want := []api.SourceReaderSpan{{From: 8, To: 11}}
	if !reflect.DeepEqual(old, want) || !reflect.DeepEqual(next, want) {
		t.Fatalf("UTF16 spans = %v / %v", old, next)
	}
}

func TestInlineChangesRewrittenLicenseParagraph(t *testing.T) {
	before := "Trademarks: Painted Wolf is a trademark of Painted Wolf LLC, along with the\nproduct name Painted Wolf Code and the project's logos, mascots, and brand\nartwork. This license grants copyright permissions for source code; it\ndoes not grant trademark rights. What redistributors may do with the marks\nis in docs/trademarks.md. That policy gives themes limited permission to alter\nthe app's visual identity, including the fixed Painted Wolf Code identity's\ntheme-controlled colors and logo-glyph visibility, through the built-in theme\nsystem.\n"
	after := "Trademarks: Painted Wolf, Painted Wolf Code, and associated product logos,\nmascots, UI artwork, and other brand assets are trademarks or trade dress of\nPainted Wolf LLC. This license grants copyright permissions for source code in\nthis repository only; it does not grant trademark rights. Use of Painted Wolf\nmarks requires permission except where law allows (for example, fair use to\nidentify the software you received under this license).\n"
	old, next := inlineChanges(before, after)
	for index, text := range []string{before, after} {
		spans := old
		if index == 1 {
			spans = next
		}
		for _, span := range spans {
			for _, at := range []int{span.From, span.To} {
				if at > 0 && at < len(text) && asciiWord(text[at-1]) && asciiWord(text[at]) {
					t.Fatalf("highlight cuts a word at %d in %q: %v", at, text, spans)
				}
			}
		}
	}
}

func asciiWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// Word changes appear only where a line also keeps a word; a rewritten line,
// or one with no counterpart, is carried by its line fill alone.
func TestWordChangesNeedUnchangedText(t *testing.T) {
	for name, tc := range map[string]struct {
		before, after string
		marked        map[string]bool
	}{
		"renamed word": {
			before: "const originalName = value;\n", after: "const replacementName = value;\n",
			marked: map[string]bool{"delete": true, "insert": true},
		},
		"rewritten line": {
			before: "return bests[pick(0)];\n", after: "if (open.length === 1) throw error;\n",
			marked: map[string]bool{},
		},
		"rewritten line sharing punctuation": {
			before: "return (bests);\n", after: "throw (error);\n",
			marked: map[string]bool{},
		},
		"added line": {
			before: "first\n", after: "first\nsecond\n",
			marked: map[string]bool{},
		},
		"removed line": {
			before: "first\nsecond\n", after: "first\n",
			marked: map[string]bool{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			document, err := New(api.SourceComparisonSide{Content: tc.before}, api.SourceComparisonSide{Content: tc.after}, nil)
			testutil.FailErr(t, "prepare comparison", err)
			for _, row := range frameForTest(t, document, 0, 20, "full").Rows {
				if row.Kind == "equal" {
					continue
				}
				if got := len(row.Changed) > 0; got != tc.marked[row.Kind] {
					t.Errorf("%s row %q changed = %v, want marked %v", row.Kind, row.Text, row.Changed, tc.marked[row.Kind])
				}
			}
		})
	}
}

func TestKeepsUnchangedWordIgnoresSpaceAndPunctuation(t *testing.T) {
	text := "  🙂 (ok);\n"
	if keepsUnchangedWord(text, []api.SourceReaderSpan{{From: 6, To: 8}}) {
		t.Error("indent, emoji, and punctuation outside the spans counted as a kept word")
	}
	if !keepsUnchangedWord(text, []api.SourceReaderSpan{{From: 6, To: 7}}) {
		t.Error("an unmarked word was not counted as kept")
	}
}
