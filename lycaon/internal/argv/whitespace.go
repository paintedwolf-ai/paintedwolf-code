package argv

import (
	"strings"
	"unicode"
)

// Command parsing and rendering share these whitespace rules.

// argSeparator matches delimiters between arguments.
func argSeparator(r rune) bool { return r == ' ' || r == '\t' }

// unquotedLineBreak rejects multiline command text outside quotes.
func unquotedLineBreak(r rune) bool { return r == '\n' || r == '\r' }

// trimmedEdge matches Unicode space at command-line edges.
func trimmedEdge(r rune) bool { return unicode.IsSpace(r) }

// trimCommandLine removes the runes trimmedEdge names from both ends.
func trimCommandLine(line string) string { return strings.TrimFunc(line, trimmedEdge) }
