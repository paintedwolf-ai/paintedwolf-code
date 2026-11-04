package citationmd

import (
	"bytes"
	"regexp"
	"strings"
)

type leafKind int

const (
	leafParagraph leafKind = iota
	leafFence
	leafHTML
	leafHeading
	leafList
)

type leafShape struct {
	kind        leafKind
	fenceChar   byte
	fenceLen    int
	htmlCloser  string
	markerWidth int
}

// classifyLeaf reads what starts at a line's first content byte.
func classifyLeaf(rest []byte, paragraph, complete bool) (leafShape, bool) {
	switch first := rest[0]; {
	case first == '`' || first == '~':
		return classifyFenceOpen(rest, complete)
	case first == '<':
		if !complete {
			return leafShape{}, false
		}
		return classifyHTMLOpen(rest, paragraph), true
	case first == '#':
		return classifyATX(rest, complete)
	case first == '=':
		if !onlyBytes(rest, '=') {
			return leafShape{}, true
		}
		if !complete {
			return leafShape{}, false
		}
		if paragraph {
			return leafShape{kind: leafHeading}, true
		}
		return leafShape{}, true
	case first == '-' || first == '*' || first == '_':
		if onlyBytes(rest, first) {
			if !complete {
				return leafShape{}, false
			}
			if isThematic(rest, first) || first == '-' && paragraph {
				return leafShape{kind: leafHeading}, true
			}
		}
		if first == '_' {
			return leafShape{}, true
		}
		return classifyListMarker(rest, 1, paragraph, complete)
	case first == '+':
		return classifyListMarker(rest, 1, paragraph, complete)
	case first >= '0' && first <= '9':
		return classifyOrderedMarker(rest, paragraph, complete)
	}
	return leafShape{}, true
}

func classifyFenceOpen(rest []byte, complete bool) (leafShape, bool) {
	fence := rest[0]
	run := 0
	for run < len(rest) && rest[run] == fence {
		run++
	}
	if run == len(rest) && !complete {
		return leafShape{}, false
	}
	if run < 3 {
		return leafShape{}, true
	}
	if fence == '`' {
		// A backtick in the info string makes this an inline code span.
		if bytes.IndexByte(rest[run:], '`') >= 0 {
			return leafShape{}, true
		}
		if !complete {
			return leafShape{}, false
		}
	}
	return leafShape{kind: leafFence, fenceChar: fence, fenceLen: run}, true
}

func classifyATX(rest []byte, complete bool) (leafShape, bool) {
	run := 0
	for run < len(rest) && rest[run] == '#' {
		run++
	}
	if run == len(rest) && !complete {
		return leafShape{}, false
	}
	if run > 6 || run < len(rest) && !isSpaceByte(rest[run]) {
		return leafShape{}, true
	}
	return leafShape{kind: leafHeading}, true
}

func onlyBytes(rest []byte, marker byte) bool {
	for _, b := range rest {
		if b != marker && !isSpaceByte(b) {
			return false
		}
	}
	return true
}

func isThematic(rest []byte, marker byte) bool { return bytes.Count(rest, []byte{marker}) >= 3 }

func classifyListMarker(rest []byte, width int, paragraph, complete bool) (leafShape, bool) {
	if width == len(rest) {
		if !complete {
			return leafShape{}, false
		}
		if paragraph {
			return leafShape{}, true
		}
		return leafShape{kind: leafList, markerWidth: width}, true
	}
	if !isSpaceByte(rest[width]) {
		return leafShape{}, true
	}
	if len(bytes.TrimLeft(rest[width:], " \t")) == 0 {
		if !complete {
			return leafShape{}, false
		}
		if paragraph {
			return leafShape{}, true
		}
	}
	return leafShape{kind: leafList, markerWidth: width}, true
}

func classifyOrderedMarker(rest []byte, paragraph, complete bool) (leafShape, bool) {
	digits := 0
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits == len(rest) {
		if !complete && digits <= 9 {
			return leafShape{}, false
		}
		return leafShape{}, true
	}
	if digits > 9 || (rest[digits] != '.' && rest[digits] != ')') {
		return leafShape{}, true
	}
	// Only an item numbered one may interrupt a paragraph.
	if paragraph && !(digits == 1 && rest[0] == '1') {
		return leafShape{}, true
	}
	return classifyListMarker(rest, digits+1, paragraph, complete)
}

var (
	htmlType1  = regexp.MustCompile(`(?i)^<(script|pre|style|textarea)(?:\s.*|>.*|/>.*|)$`)
	htmlType4  = regexp.MustCompile(`^<![A-Z]+`)
	htmlType6  = regexp.MustCompile(`^<(?:/ *)?([a-zA-Z]+[a-zA-Z0-9\-]*)(?: .*|>.*|/>.*|)$`)
	attrSource = `(?:[\r\n \t]+[a-zA-Z_:][a-zA-Z0-9:._-]*(?:[\r\n \t]*=[\r\n \t]*(?:[^"'=<>` + "`" + `\x00-\x20]+|'[^']*'|"[^"]*"))?)`
	htmlType7  = regexp.MustCompile(`^<(/ *)?([a-zA-Z]+[a-zA-Z0-9\-]*)(` + attrSource + `*) *(?:>|/>) *$`)
)

const blockTagNames = "|address|article|aside|base|basefont|blockquote|body|caption|center|col|colgroup|dd|details|dialog|dir|div|dl|dt|fieldset|figcaption|figure|footer|form|frame|frameset|h1|h2|h3|h4|h5|h6|head|header|hr|html|iframe|legend|li|link|main|menu|menuitem|meta|nav|noframes|ol|optgroup|option|p|param|search|section|summary|table|tbody|td|tfoot|th|thead|title|tr|track|ul|"

// classifyHTMLOpen applies the CommonMark HTML block start conditions.
func classifyHTMLOpen(rest []byte, paragraph bool) leafShape {
	switch {
	case htmlType1.Match(rest):
		tag := htmlType1.FindSubmatch(rest)[1]
		return leafShape{kind: leafHTML, htmlCloser: "</" + strings.ToLower(string(tag)) + ">"}
	case bytes.HasPrefix(rest, []byte("<!--")):
		return leafShape{kind: leafHTML, htmlCloser: "-->"}
	case bytes.HasPrefix(rest, []byte("<?")):
		return leafShape{kind: leafHTML, htmlCloser: "?>"}
	case bytes.HasPrefix(rest, []byte("<![CDATA[")):
		return leafShape{kind: leafHTML, htmlCloser: "]]>"}
	case htmlType4.Match(rest):
		return leafShape{kind: leafHTML, htmlCloser: ">"}
	}
	if match := htmlType6.FindSubmatch(rest); len(match) > 1 && strings.Contains(blockTagNames, "|"+strings.ToLower(string(match[1]))+"|") {
		return leafShape{kind: leafHTML}
	}
	if !paragraph && htmlType7.Match(rest) {
		return leafShape{kind: leafHTML}
	}
	return leafShape{}
}
