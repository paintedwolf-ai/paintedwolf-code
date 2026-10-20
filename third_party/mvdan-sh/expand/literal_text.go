package expand

import "unicode/utf8"

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
