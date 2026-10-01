package sourcecomparison

import (
	"reflect"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompactSyntaxMatchesCompleteLexerAcrossSeekBlocks(t *testing.T) {
	text := "/* multiline\ncomment */\n" + strings.Repeat("const value = `😀 and a string`; // note\n", 300)
	index, err := syntaxSpans(t.Context(), "source.ts", text)
	testutil.FailErr(t, "prepare syntax", err)
	iterator, err := chroma.Coalesce(lexers.Match("source.ts")).Tokenise(nil, text)
	testutil.FailErr(t, "lex syntax oracle", err)
	var tokens []api.SourceReaderToken
	at := 0
	for token := iterator(); token != chroma.EOF; token = iterator() {
		end := at + width(token.Value)
		if kind := syntaxKind(token.Type); kind != "" && end > at {
			tokens = append(tokens, api.SourceReaderToken{From: at, To: end, Kind: kind})
		}
		at = end
	}
	if len(index.blocks) < 3 {
		t.Fatal("fixture did not span seek blocks")
	}
	for from := 0; from < width(text); from += 73 {
		var expected []api.SourceReaderToken
		for _, token := range tokens {
			if token.To <= from || token.From >= from+131 {
				continue
			}
			token.From, token.To = max(0, token.From-from), min(131, token.To-from)
			expected = append(expected, token)
		}
		actual := rowSyntax(index, from, 131)
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("syntax at %d differs: got=%v want=%v", from, actual, expected)
		}
	}
	if retained := len(index.data) + len(index.blocks)*12; retained >= len(tokens)*12 {
		t.Fatalf("syntax index did not compact coordinates: %d bytes for %d tokens", retained, len(tokens))
	}
}
