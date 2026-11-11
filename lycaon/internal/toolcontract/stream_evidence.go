package toolcontract

// StreamSourceMapping is a candidate source range carried beside exact bytes.
// It records no model delivery and establishes no conversational read base.
// Source keeps the original captured representation and digest unchanged;
// SourceStartByte/SourceEndByte select mapped bytes inside that capture.
// A delivered page still needs the source owner's normal response attribution.
type StreamSourceMapping struct {
	Source                         SourcePin
	SourceStartByte, SourceEndByte int64
	OutputStartByte, OutputEndByte int64
	// PrintedLine is the line number the producer printed beside these bytes, or
	// zero. It says what the reader saw, which need not be the source's own number.
	PrintedLine int64 `json:",omitempty"`
}
