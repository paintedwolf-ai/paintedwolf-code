package documentcore

// LineSpan is a complete inclusive range of source lines delivered to a reader.
type LineSpan struct{ StartLine, EndLine int }

// ReadBasis freezes a delivered revision and its coverage. Whole marks a
// complete document; otherwise only Spans were delivered.
type ReadBasis struct {
	Revision int64
	Whole    bool
	Spans    []LineSpan
}
