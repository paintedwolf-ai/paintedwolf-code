package expand_test

import (
	"testing"

	"mvdan.cc/sh/v3/expand"
)

type quotedPiece struct {
	text    string
	escaped bool
}

func TestRemoveQuotesSelectsBackslashRules(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		text    string
		context expand.QuoteContext
		want    []quotedPiece
	}{
		{"unquoted escapes any character", `a\*b\ c`, expand.UnquotedText, []quotedPiece{{"a", false}, {"*", true}, {"b", false}, {" ", true}, {"c", false}}},
		{"unquoted escapes a whole scalar", `\世x`, expand.UnquotedText, []quotedPiece{{"", false}, {"世", true}, {"x", false}}},
		{"unquoted keeps a trailing backslash", `a\`, expand.UnquotedText, []quotedPiece{{`a\`, false}}},
		{"double quotes escape only specials", `\"\$\\\n\*`, expand.DoubleQuotedText, []quotedPiece{{"", false}, {`"`, true}, {"", false}, {"$", true}, {"", false}, {`\`, true}, {`\n\*`, false}}},
		{"here-documents keep a quoted double quote", `\"\$x`, expand.HereDocumentText, []quotedPiece{{`\"`, false}, {"$", true}, {"x", false}}},
		{"plain text is one visit", "plain", expand.DoubleQuotedText, []quotedPiece{{"plain", false}}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			var got []quotedPiece
			expand.RemoveQuotes(fixture.text, fixture.context, func(text string, escaped bool) {
				got = append(got, quotedPiece{text, escaped})
			})
			if len(got) != len(fixture.want) {
				t.Fatalf("pieces = %+v, want %+v", got, fixture.want)
			}
			for i := range got {
				if got[i] != fixture.want[i] {
					t.Fatalf("pieces = %+v, want %+v", got, fixture.want)
				}
			}
		})
	}
}
