package agentpresence

import "github.com/lycaon/lycaon/pkg/api"

// OutputLimit is how host output limits changed what a call returned to the model.
type OutputLimit struct {
	// Spilled means the model received a preview or refusal instead of the output.
	Spilled bool
	// KeptEntries is how many leading entries of a paginated result remained; nil when untrimmed.
	KeptEntries *int
	// KeptThroughLine is the last line of a content read that remained; zero keeps every line.
	KeptThroughLine int
}

// WithinLimit keeps only the reads, spans, and lines the model received.
func WithinLimit(reads []Read, limit OutputLimit) []Read {
	switch {
	case limit.Spilled:
		return nil
	case limit.KeptEntries != nil:
		return keepEntries(reads, *limit.KeptEntries)
	case limit.KeptThroughLine > 0:
		return keepThroughLine(reads, limit.KeptThroughLine)
	default:
		return reads
	}
}

func keepEntries(reads []Read, entries int) []Read {
	out := make([]Read, 0, len(reads))
	for _, read := range reads {
		if entries <= 0 {
			break
		}
		items := read.ItemSpans
		if len(items) == 0 {
			items = []int{len(read.Spans)}
		}
		spans, kept := 0, 0
		for _, n := range items {
			if kept == entries {
				break
			}
			spans += n
			kept++
		}
		entries -= kept
		read.Spans, read.ItemSpans = read.Spans[:min(spans, len(read.Spans))], items[:kept]
		out = append(out, read)
	}
	return out
}

func keepThroughLine(reads []Read, last int) []Read {
	out := make([]Read, 0, len(reads))
	for _, read := range reads {
		if read.Extent == api.AgentPresenceExtentWholeFile {
			read.Extent, read.Spans = api.AgentPresenceExtentRange, []Span{{StartLine: 1, EndLine: last}}
			out = append(out, read)
			continue
		}
		kept := read.Spans[:0:0]
		for _, span := range read.Spans {
			if span.StartLine > last {
				continue
			}
			span.EndLine = min(span.EndLine, last)
			kept = append(kept, span)
		}
		if len(kept) > 0 {
			read.Spans = kept
			out = append(out, read)
		}
	}
	return out
}
