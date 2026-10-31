package expand

import "unicode/utf8"

// QuoteContext selects the backslash rules that apply to literal word text.
type QuoteContext uint8

const (
	// UnquotedText removes a backslash before any character.
	UnquotedText QuoteContext = iota
	// DoubleQuotedText removes a backslash only before $, `, " or \.
	DoubleQuotedText
	// HereDocumentText removes a backslash only before $, ` or \.
	HereDocumentText
)

// RemoveQuotes walks literal text under the selected quote-removal rules, in
// order, without allocating. escaped reports text quoted by a backslash. It
// performs no parameter, substitution, tilde, brace or pathname expansion.
func RemoveQuotes(text string, context QuoteContext, visit func(text string, escaped bool)) {
	quote := quoteNone
	switch context {
	case DoubleQuotedText:
		quote = quoteDouble
	case HereDocumentText:
		quote = quoteHeredoc
	}
	literalText(text, quote, visit)
}

// literalText walks the selected quote-removal rules without allocating one
// field fragment per escape. The flag identifies text quoted by a backslash.
func literalText(value string, quote quoteLevel, visit func(string, bool)) {
	start := 0
	for i := 0; i+1 < len(value); i++ {
		if value[i] != '\\' {
			continue
		}
		size := 1
		if quote == quoteNone {
			_, size = utf8.DecodeRuneInString(value[i+1:])
		} else {
			next := value[i+1]
			if next != '\\' && next != '$' && next != '`' && !(quote == quoteDouble && next == '"') {
				continue
			}
		}
		visit(value[start:i], false)
		visit(value[i+1:i+1+size], true)
		i += size
		start = i + 1
	}
	visit(value[start:], false)
}
