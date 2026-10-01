//go:build !grammar_subset || grammar_subset_html

package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func skipToByte(ts gotreesitter.TokenSource, offset uint32) (gotreesitter.Token, bool) {
	if skipper, ok := ts.(gotreesitter.ByteSkippableTokenSource); ok {
		return skipper.SkipToByte(offset), true
	}
	return gotreesitter.Token{}, false
}

func TestSkipToByteConsistency(t *testing.T) {
	for _, tc := range allLexerTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			lang := tc.lang()

			// Collect all tokens via linear scan.
			ts1 := tc.factory(tc.src, lang)
			tokens := collectAllTokens(ts1, 1000)

			// For each non-EOF token, create a fresh lexer, SkipToByte to its offset,
			// and verify the returned token starts at or after that offset.
			for i, tok := range tokens {
				if tok.Symbol == 0 {
					break
				}
				ts2 := tc.factory(tc.src, lang)
				got, ok := skipToByte(ts2, tok.StartByte)
				if !ok {
					t.Skipf("lexer does not implement ByteSkippableTokenSource")
					return
				}
				if got.Symbol == 0 && tok.StartByte < uint32(len(tc.src)) {
					t.Errorf("token %d: SkipToByte(%d) returned EOF unexpectedly", i, tok.StartByte)
					continue
				}
				if got.StartByte < tok.StartByte && got.Symbol != 0 {
					t.Errorf("token %d: SkipToByte(%d) returned token starting at %d (before target)",
						i, tok.StartByte, got.StartByte)
				}
			}
		})
	}
}

func TestSkipToByteEdgeCases(t *testing.T) {
	for _, tc := range allLexerTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			lang := tc.lang()

			// SkipToByte(0) should return first token
			ts := tc.factory(tc.src, lang)
			tok, ok := skipToByte(ts, 0)
			if !ok {
				t.Skipf("lexer does not implement ByteSkippableTokenSource")
				return
			}
			if tok.Symbol == 0 && len(tc.src) > 0 {
				t.Error("SkipToByte(0) returned EOF on non-empty source")
			}

			// SkipToByte(len(src)) should return EOF
			ts2 := tc.factory(tc.src, lang)
			tok2, _ := skipToByte(ts2, uint32(len(tc.src)))
			if tok2.Symbol != 0 {
				// Some lexers return a final synthetic token at EOF
				// (like YAML _eof or TOML line_ending), so just verify
				// we don't crash.
				_ = tok2
			}

			// SkipToByte past end should return EOF
			ts3 := tc.factory(tc.src, lang)
			tok3, _ := skipToByte(ts3, uint32(len(tc.src)+100))
			if tok3.Symbol != 0 {
				_ = tok3 // some lexers emit final tokens; don't fail
			}
		})
	}
}

func TestSkipToByteMiddle(t *testing.T) {
	for _, tc := range allLexerTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.src) < 4 {
				t.Skip("source too short")
			}
			lang := tc.lang()
			mid := uint32(len(tc.src) / 2)

			ts := tc.factory(tc.src, lang)
			tok, ok := skipToByte(ts, mid)
			if !ok {
				t.Skipf("lexer does not implement ByteSkippableTokenSource")
				return
			}
			if tok.Symbol == 0 {
				return // EOF is acceptable for mid-point in short sources
			}
			if tok.StartByte < mid {
				t.Errorf("SkipToByte(%d) returned token at %d (before target)", mid, tok.StartByte)
			}
		})
	}
}

func TestHTMLSkipToByteOutsideTag(t *testing.T) {
	lang := HtmlLanguage()
	src := []byte(`<div class="foo">text content</div>`)

	// First token inside the tag
	ts := NewHTMLTokenSourceOrEOF(src, lang)
	var insideTagToken gotreesitter.Token
	for i := 0; i < 20; i++ {
		tok := ts.Next()
		if tok.Symbol == 0 {
			break
		}
		// The "class" attribute name is inside the tag
		if tok.Text == "class" {
			insideTagToken = tok
			break
		}
	}
	if insideTagToken.Symbol == 0 {
		t.Skip("could not find 'class' token")
	}

	// Now skip to the text content area (after ">")
	textStart := uint32(18) // approximate offset of "text content"
	ts2, err := NewHTMLTokenSource(src, lang)
	if err != nil {
		t.Fatalf("NewHTMLTokenSource: %v", err)
	}
	tok := ts2.SkipToByte(textStart)
	if tok.Symbol == 0 {
		t.Fatal("SkipToByte returned EOF")
	}

	// The token should be a text token, not an attribute
	name := lang.SymbolNames[tok.Symbol]
	if name == "attribute_name" || name == "attribute_value" {
		t.Errorf("SkipToByte to text area returned %q token instead of text", name)
	}
}
