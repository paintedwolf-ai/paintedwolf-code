package visualscreen

import (
	"bytes"
	"encoding/xml"
	"strings"
)

// screenedSVGAttrs are the attributes whose values can surface as visible or
// announced text.
var screenedSVGAttrs = map[string]struct{}{
	"id": {}, "title": {}, "alt": {}, "aria-label": {},
}

// xmlNamespace is what the decoder resolves the reserved xml: prefix to.
const xmlNamespace = "http://www.w3.org/XML/1998/namespace"

// svgTextContentElements continue the rendered run of their <text> ancestor.
var svgTextContentElements = map[string]struct{}{
	"tspan": {}, "textpath": {}, "a": {}, "tref": {}, "altglyph": {},
}

// svgFrame is one open element; run is set when its character data renders
// as part of the enclosing <text> element's run.
type svgFrame struct {
	name     string
	run      bool
	owner    bool
	preserve bool
}

// extractSVGText reads text nodes, comments, and labelling attributes, keeping
// the source byte range each value was read from. Character data a <text>
// element renders contiguously, across tspans and comments, is read as one
// line; a descendant carrying y or dy starts a new line.
func extractSVGText(raw []byte, b *textBuilder) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity

	var stack []svgFrame
	var run svgTextRun
	for {
		start := int(decoder.InputOffset())
		tok, err := decoder.Token()
		if err != nil {
			b.addRun(run.take())
			return
		}
		end := int(decoder.InputOffset())
		if start < 0 || end > len(raw) || start > end {
			continue
		}
		src := raw[start:end]
		switch t := tok.(type) {
		case xml.StartElement:
			frame := openSVGFrame(stack, t)
			if frame.run && !frame.owner && startsSVGLine(t) {
				b.addRun(run.take())
			}
			stack = append(stack, frame)
			addSVGAttrs(b, t, src, start)
		case xml.EndElement:
			stack = closeSVGFrame(stack, t.Name.Local, func() { b.addRun(run.take()) })
		case xml.CharData:
			rawStart := -1
			text := string(t)
			if inner, offset, ok := verbatimText(src, text); ok {
				rawStart = start + offset
				text = inner
			}
			if n := len(stack); n > 0 && stack[n-1].run {
				run.add(text, rawStart, stack[n-1].preserve)
				continue
			}
			b.add(segment{kind: segmentMarkup, rawStart: rawStart, span: -1}, text)
		case xml.Comment:
			seg := segment{kind: segmentMarkup, rawStart: -1, span: -1}
			text := string(t)
			if bytes.HasPrefix(src, []byte("<!--")) && bytes.HasSuffix(src, []byte("-->")) &&
				string(src[4:len(src)-3]) == text {
				seg.rawStart = start + 4
			}
			b.add(seg, text)
		}
	}
}

func openSVGFrame(stack []svgFrame, t xml.StartElement) svgFrame {
	name := strings.ToLower(t.Name.Local)
	frame := svgFrame{name: name}
	if n := len(stack); n > 0 {
		frame.preserve = stack[n-1].preserve
		if _, content := svgTextContentElements[name]; content && stack[n-1].run {
			frame.run = true
		}
	}
	if name == "text" {
		inRun := len(stack) > 0 && stack[len(stack)-1].run
		frame.run, frame.owner = !inRun, !inRun
	}
	for _, attr := range t.Attr {
		if attr.Name.Local == "space" && (attr.Name.Space == "xml" || attr.Name.Space == xmlNamespace) {
			frame.preserve = attr.Value == "preserve"
		}
	}
	return frame
}

// startsSVGLine reports whether a text content element moves to a new line.
func startsSVGLine(t xml.StartElement) bool {
	for _, attr := range t.Attr {
		switch strings.ToLower(attr.Name.Local) {
		case "y", "dy":
			return true
		}
	}
	return false
}

// closeSVGFrame pops to the innermost open element named local, flushing the
// run when its owning <text> closes. An unmatched end tag is ignored.
func closeSVGFrame(stack []svgFrame, local string, flush func()) []svgFrame {
	name := strings.ToLower(local)
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].name != name {
			continue
		}
		for j := len(stack) - 1; j >= i; j-- {
			if stack[j].owner {
				flush()
			}
		}
		return stack[:i]
	}
	return stack
}

func addSVGAttrs(b *textBuilder, t xml.StartElement, src []byte, start int) {
	cursor := 0
	for _, attr := range t.Attr {
		if _, ok := screenedSVGAttrs[strings.ToLower(attr.Name.Local)]; !ok {
			continue
		}
		valueStart, valueEnd, next := findAttrValue(src, attr.Name.Local, cursor)
		seg := segment{kind: segmentMarkup, rawStart: -1, span: -1}
		if valueStart >= 0 {
			cursor = next
			if string(src[valueStart:valueEnd]) == attr.Value {
				seg.rawStart = start + valueStart
			}
		}
		b.add(seg, attr.Value)
	}
}

// runPart is one verbatim piece of a rendered run.
type runPart struct {
	text        string
	rawStart    int
	spaceBefore bool
}

// svgTextRun applies SVG whitespace handling to a <text> element's character
// data: newlines are dropped (spaces under xml:space="preserve"), and spaces
// and tabs collapse to one space. Each piece keeps its source offset.
type svgTextRun struct {
	parts []runPart
	space bool
}

func (r *svgTextRun) add(text string, rawStart int, preserve bool) {
	i := 0
	for i < len(text) {
		c := text[i]
		if c == ' ' || c == '\t' || ((c == '\n' || c == '\r') && preserve) {
			r.space = true
			i++
			continue
		}
		if c == '\n' || c == '\r' {
			i++
			continue
		}
		j := i
		for j < len(text) && !isXMLSpace(text[j]) {
			j++
		}
		part := runPart{text: text[i:j], rawStart: -1, spaceBefore: r.space && len(r.parts) > 0}
		if rawStart >= 0 {
			part.rawStart = rawStart + i
		}
		r.parts = append(r.parts, part)
		r.space = false
		i = j
	}
}

func (r *svgTextRun) take() []runPart {
	parts := r.parts
	*r = svgTextRun{}
	return parts
}

// verbatimText reports whether decoded character data appears byte for byte
// in its source, either directly or inside a CDATA section.
func verbatimText(src []byte, decoded string) (string, int, bool) {
	if string(src) == decoded {
		return decoded, 0, true
	}
	const open, closing = "<![CDATA[", "]]>"
	if bytes.HasPrefix(src, []byte(open)) && bytes.HasSuffix(src, []byte(closing)) &&
		string(src[len(open):len(src)-len(closing)]) == decoded {
		return decoded, len(open), true
	}
	return "", 0, false
}

// findAttrValue locates the quoted value of the next attribute named local
// (with any prefix) at or after from in a raw start tag.
func findAttrValue(tag []byte, local string, from int) (int, int, int) {
	lower := bytes.ToLower(tag)
	name := []byte(strings.ToLower(local))
	for i := from; i < len(lower); {
		idx := bytes.Index(lower[i:], name)
		if idx < 0 {
			return -1, -1, from
		}
		pos := i + idx
		i = pos + len(name)
		if pos == 0 {
			continue
		}
		if prev := lower[pos-1]; prev != ' ' && prev != '\t' && prev != '\n' && prev != '\r' && prev != ':' {
			continue
		}
		j := i
		for j < len(tag) && isXMLSpace(tag[j]) {
			j++
		}
		if j >= len(tag) || tag[j] != '=' {
			continue
		}
		j++
		for j < len(tag) && isXMLSpace(tag[j]) {
			j++
		}
		if j >= len(tag) || (tag[j] != '"' && tag[j] != '\'') {
			continue
		}
		quote := tag[j]
		valueStart := j + 1
		closeIdx := bytes.IndexByte(tag[valueStart:], quote)
		if closeIdx < 0 {
			return -1, -1, from
		}
		valueEnd := valueStart + closeIdx
		return valueStart, valueEnd, valueEnd + 1
	}
	return -1, -1, from
}

func isXMLSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
