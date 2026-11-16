package httpaction

// maxInlineBody bounds what one response puts in front of the model. Anything
// past it lands whole under host data and the result names the file.
const maxInlineBody = 128 << 10

// bodyPlacement is where a response body ended up and what the result may
// therefore claim about it.
type bodyPlacement struct {
	inline   string
	encoding string
	omitted  bool
	// spillPath is host-data relative and readable with read or jq.
	spillPath string
	// spillFailed marks an overflow that could not be landed, so the result
	// reports the body as unavailable.
	spillFailed bool
}
