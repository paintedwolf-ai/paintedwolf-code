package commandsurface

import (
	"bytes"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func programEncodingDiagnostic(source []byte, sourced bool) *SyntaxDiagnostic {
	code := "COMMAND_SYNTAX_INVALID"
	if sourced {
		code = "COMMAND_OPERAND_INVALID"
	}
	failure := func(kind, message string, start, end, line, column int) *SyntaxDiagnostic {
		return &SyntaxDiagnostic{Code: code, Span: toolcontract.SourceSpan{StartByte: start, EndByte: end, Line: line, Column: column}, Expected: "UTF-8 source without BOM or NUL", Parser: &toolcontract.ParserDiagnostic{Kind: kind, Message: message}}
	}
	if bytes.HasPrefix(source, []byte{0xef, 0xbb, 0xbf}) {
		return failure("utf8_bom", "source begins with a UTF-8 byte-order mark", 0, 3, 1, 1)
	}
	line, column := 1, 1
	for offset := 0; offset < len(source); {
		r, width := utf8.DecodeRune(source[offset:])
		if source[offset] == 0 {
			return failure("nul_byte", "source contains a NUL byte", offset, offset+1, line, column)
		}
		if r == utf8.RuneError && width == 1 {
			return failure("invalid_utf8", "source contains invalid UTF-8", offset, offset+1, line, column)
		}
		if r == '\n' {
			line++
			column = 1
		} else {
			column += width
		}
		offset += width
	}
	return nil
}
