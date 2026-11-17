package citationmd

// LiteralZones returns the source's literal zones, under Parse's budgets. Past a
// budget they are the coarse zones of a source that was not structurally parsed.
func LiteralZones(source string) []Zone {
	return Parse(source, Options{}).Zones
}

// TextRanges returns the complement of LiteralZones: the only ranges in which
// custom nodes may begin.
func TextRanges(source string) []Span {
	doc := Parse(source, Options{})
	var out []Span
	at := doc.Body.Start
	for _, zone := range doc.Zones {
		if zone.Span.Start > at {
			out = append(out, Span{at, zone.Span.Start})
		}
		at = max(at, zone.Span.End)
	}
	if at < doc.Body.End {
		out = append(out, Span{at, doc.Body.End})
	}
	return out
}
