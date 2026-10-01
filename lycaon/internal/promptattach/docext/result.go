package docext

// Result is the output of a single document extraction.
type Result struct {
	Text string
	// UnitCount is the page, slide, or sheet count.
	UnitCount int
	// ScannedNoText marks a document without native text.
	ScannedNoText bool
}
