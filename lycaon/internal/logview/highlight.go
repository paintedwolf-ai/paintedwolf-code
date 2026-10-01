package logview

import (
	"strings"

	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// maxHighlightBytes caps how much content is syntax-highlighted, so opening a huge
// system prompt or tool result stays snappy (it falls back to plain text).
const maxHighlightBytes = 384 * 1024

var (
	hlFormatter = formatters.Get("terminal256")
	hlStyle     = styles.Get("nord")
)

func init() {
	if hlFormatter == nil {
		hlFormatter = formatters.Fallback
	}
	if hlStyle == nil {
		hlStyle = styles.Fallback
	}
}

// highlightSource returns src colorized for a 256-color terminal with the named
// chroma lexer, or src unchanged if highlighting is unavailable or fails.
func highlightSource(src, lexer string) string {
	l := lexers.Get(lexer)
	if l == nil {
		return src
	}
	it, err := l.Tokenise(nil, src)
	if err != nil {
		return src
	}
	var b strings.Builder
	if err := hlFormatter.Format(&b, hlStyle, it); err != nil {
		return src
	}
	return strings.TrimRight(b.String(), "\n")
}
