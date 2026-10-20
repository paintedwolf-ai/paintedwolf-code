package toolexecution

import (
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// Complete only absent operation attribution. Actual owner codes, operands and
// more precise nested-source locations remain authoritative; wrapped errors
// keep their original identity. Outer command projection belongs to its caller.
func completeNativeRejection(bound toolcontract.BoundOperation, err error) {
	rejected := toolrejection.AsToolReject(err)
	if rejected == nil {
		return
	}
	definition := bound.Definition()
	if definition.Digest() == "" {
		return
	}
	facts, invocation := definition.Facts(), bound.Invocation()
	toolrejection.CompleteFailureMetadata(rejected, facts.Identity, facts.Contract.Owner)
	// A source location is one unit. Mixing an owner's nested start with the
	// enclosing program's end would fabricate a range.
	for _, key := range []string{"source_start_byte", "source_end_byte", "source_line", "source_column"} {
		if _, present := rejected.Data[key]; present {
			return
		}
	}
	rejected.Data["source_start_byte"] = invocation.Span.StartByte
	rejected.Data["source_end_byte"] = invocation.Span.EndByte
	rejected.Data["source_line"] = invocation.Span.Line
	rejected.Data["source_column"] = invocation.Span.Column
}
