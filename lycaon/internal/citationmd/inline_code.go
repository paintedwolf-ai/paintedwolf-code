package citationmd

import "strings"

// addCodeCitation takes a standalone code span as a citation when it is exactly a
// coordinate on an observed path and the evidence supports it. Anything else stays
// code, with no defect: a span cannot be refused for what it merely mentions.
func (b *documentBuilder) addCodeCitation(zone Zone) bool {
	if b.opts.CodeCitation == nil || len(b.doc.Occurrences) >= b.budget {
		return false
	}
	body, ok := standaloneCodeBody(b.doc.Source, zone.Span)
	if !ok {
		return false
	}
	path, start, end, ok := splitCoordinate(body)
	if !ok || start == "" || !validBarePath(path) || !b.opts.known(path) {
		return false
	}
	citation := &Citation{Path: path}
	b.canonicalize(citation)
	if setLines(citation, start, end) != "" || !b.opts.CodeCitation(*citation) {
		return false
	}
	first := len(b.doc.Occurrences)
	b.doc.Occurrences = append(b.doc.Occurrences, Occurrence{Index: first, Kind: KindCitation, Span: zone.Span, Node: zone.Span, Token: body, Citation: citation, InlineCode: true})
	b.doc.parts = append(b.doc.parts, part{kind: partNode, span: zone.Span, first: first, count: 1})
	return true
}

// standaloneCodeBody returns a single-backtick span's text when no word touches it,
// so that `+"`a.go:1`"+`_suffix and prefix-`+"`a.go:1`"+` are not citations.
func standaloneCodeBody(source string, span Span) (string, bool) {
	if span.End-span.Start < 3 || source[span.Start] != '`' || source[span.End-1] != '`' {
		return "", false
	}
	body := source[span.Start+1 : span.End-1]
	if strings.IndexByte(body, '`') >= 0 {
		return "", false
	}
	if span.Start > 0 && !opensCitation(source[span.Start-1]) {
		return "", false
	}
	if span.End < len(source) && !closesCitation(source[span.End]) {
		return "", false
	}
	return body, true
}

func opensCitation(b byte) bool {
	return isSpaceOrNewline(b) || strings.IndexByte(`([{*_~"'>`+"|", b) >= 0
}

func closesCitation(b byte) bool {
	return isSpaceOrNewline(b) || strings.IndexByte(`)]}*_~"'.,;:!?|`, b) >= 0
}
