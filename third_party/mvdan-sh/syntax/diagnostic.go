package syntax

// ErrorKind describes parser state without interpreting diagnostic text.
type ErrorKind string

const (
	InvalidSyntax     ErrorKind = "invalid_syntax"
	UnclosedQuote     ErrorKind = "unclosed_quote"
	UnclosedHeredoc   ErrorKind = "unclosed_heredoc"
	ExpectedCloser    ErrorKind = "expected_closer"
	ExpectedFollowing ErrorKind = "expected_following"
)

// ErrorDiagnostic keeps the expected form and the token where parsing stopped.
type ErrorDiagnostic struct {
	Kind     ErrorKind
	Expected string
	Found    string
	At       Pos
}

func (p *Parser) diagnosticErr(pos Pos, diagnostic ErrorDiagnostic, format string, args ...any) {
	p.enterFrame()
	defer p.leaveFrame()
	p.reserveHeap(1024)
	p.errPass(ParseError{
		Filename: p.f.Name, Pos: pos, Text: p.budgetFormat(format, args...),
		Diagnostic: diagnostic, Incomplete: p.tok == _EOF && p.Incomplete(),
	})
}
