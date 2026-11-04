package citationmd

import (
	"bytes"
	"regexp"
	"strings"
)

// lineKind is how a streamed line presents.
type lineKind int

const (
	lineInline lineKind = iota
	lineLiteral
	lineBlank
)

// lineDecision classifies one line. prefix is the container markup before its
// inline content. continuing means it extends the open paragraph.
type lineDecision struct {
	kind       lineKind
	prefix     int
	continuing bool
}

type container struct {
	quote bool
	// indent is an item's absolute content column.
	indent int
}

// blockTracker follows CommonMark block structure over complete line heads,
// tracking only what decides literal zones. Fixtures hold it to the structural
// parse: both must name the same literal lines.
type blockTracker struct {
	containers []container
	fenceChar  byte
	fenceLen   int
	// htmlOpen is an HTML block; an empty htmlCloser ends it at a blank line.
	htmlOpen   bool
	htmlCloser string
	htmlTail   []byte
	htmlDone   bool
	paragraph  bool
}

// lineCursor walks a line's columns, with tabs at multiples of four.
type lineCursor struct {
	line []byte
	pos  int
	col  int
}

func (c *lineCursor) atEnd() bool { return c.pos >= len(c.line) }

func (c *lineCursor) stepSpace() {
	if c.line[c.pos] == '\t' {
		c.col += 4 - c.col%4
	} else {
		c.col++
	}
	c.pos++
}

func isSpaceByte(b byte) bool { return b == ' ' || b == '\t' }

// peekSpace returns the cursor past whitespace without moving this one.
func (c lineCursor) peekSpace() lineCursor {
	for !c.atEnd() && isSpaceByte(c.line[c.pos]) {
		c.stepSpace()
	}
	return c
}

// skipToColumn consumes whitespace up to a column.
func (c *lineCursor) skipToColumn(column int) {
	for !c.atEnd() && c.col < column && isSpaceByte(c.line[c.pos]) {
		c.stepSpace()
	}
}

// consumeQuote takes `>` with one optional following space.
func (c *lineCursor) consumeQuote() {
	c.pos++
	c.col++
	if !c.atEnd() && isSpaceByte(c.line[c.pos]) {
		c.stepSpace()
	}
}

// classify decides a line from its head. ok is false while the head cannot
// decide yet and more bytes may change the answer; complete means the head is
// the whole line. State changes only on a decision.
func (t *blockTracker) classify(line []byte, complete bool) (decision lineDecision, ok bool) {
	cursor := lineCursor{line: line}
	matched, ok := t.matchContainers(&cursor, complete)
	if !ok {
		return lineDecision{}, false
	}
	all := matched == len(t.containers)
	if t.fenceLen > 0 && all {
		return t.classifyFenceLine(cursor, complete)
	}
	if t.htmlOpen && all {
		return t.classifyHTMLLine(cursor, complete)
	}
	return t.classifyNewBlock(cursor, matched, complete)
}

func (t *blockTracker) matchContainers(c *lineCursor, complete bool) (matched int, ok bool) {
	for _, item := range t.containers {
		past := c.peekSpace()
		if past.atEnd() && !complete {
			return 0, false
		}
		if item.quote {
			if past.atEnd() || past.col-c.col > 3 || line(past) != '>' {
				return matched, true
			}
			*c = past
			c.consumeQuote()
		} else {
			if !past.atEnd() && past.col < item.indent {
				return matched, true
			}
			if past.atEnd() {
				*c = past
			} else {
				c.skipToColumn(item.indent)
			}
		}
		matched++
	}
	return matched, true
}

func line(c lineCursor) byte { return c.line[c.pos] }

func (t *blockTracker) classifyFenceLine(c lineCursor, complete bool) (lineDecision, bool) {
	past := c.peekSpace()
	if past.atEnd() {
		if !complete {
			return lineDecision{}, false
		}
		return lineDecision{kind: lineLiteral}, true
	}
	if past.col-c.col >= 4 || line(past) != t.fenceChar {
		return lineDecision{kind: lineLiteral}, true
	}
	end := past.pos
	for end < len(c.line) && c.line[end] == t.fenceChar {
		end++
	}
	if end == len(c.line) && !complete {
		return lineDecision{}, false
	}
	if end-past.pos < t.fenceLen || len(bytes.TrimLeft(c.line[end:], " \t")) > 0 {
		return lineDecision{kind: lineLiteral}, true
	}
	if !complete {
		return lineDecision{}, false
	}
	t.fenceChar, t.fenceLen = 0, 0
	return lineDecision{kind: lineLiteral}, true
}

func (t *blockTracker) classifyHTMLLine(c lineCursor, complete bool) (lineDecision, bool) {
	if t.htmlCloser != "" {
		return lineDecision{kind: lineLiteral}, true
	}
	past := c.peekSpace()
	if !past.atEnd() {
		return lineDecision{kind: lineLiteral}, true
	}
	if !complete {
		return lineDecision{}, false
	}
	t.htmlOpen = false
	return lineDecision{kind: lineBlank, prefix: len(c.line)}, true
}

// classifyNewBlock handles a line with no open leaf to continue.
func (t *blockTracker) classifyNewBlock(c lineCursor, matched int, complete bool) (lineDecision, bool) {
	containers := append([]container(nil), t.containers...)
	paragraph := t.paragraph
	commit := func() {
		t.containers = containers
		t.fenceChar, t.fenceLen = 0, 0
		t.htmlOpen = false
	}
	closeUnmatched := func() { containers = containers[:matched] }
	for {
		past := c.peekSpace()
		if past.atEnd() {
			if !complete {
				return lineDecision{}, false
			}
			closeUnmatched()
			commit()
			t.paragraph = false
			return lineDecision{kind: lineBlank, prefix: len(c.line)}, true
		}
		if past.col-c.col >= 4 {
			if paragraph {
				commit()
				t.paragraph = true
				return lineDecision{kind: lineInline, prefix: past.pos, continuing: true}, true
			}
			closeUnmatched()
			commit()
			return lineDecision{kind: lineLiteral}, true
		}
		first := line(past)
		rest := c.line[past.pos:]
		if first == '>' {
			closeUnmatched()
			containers = append(containers, container{quote: true})
			matched = len(containers)
			paragraph = false
			c = past
			c.consumeQuote()
			continue
		}
		block, decided := classifyLeaf(rest, paragraph, complete)
		if !decided {
			return lineDecision{}, false
		}
		switch block.kind {
		case leafFence:
			closeUnmatched()
			commit()
			t.fenceChar, t.fenceLen = block.fenceChar, block.fenceLen
			t.paragraph = false
			return lineDecision{kind: lineLiteral}, true
		case leafHTML:
			closeUnmatched()
			commit()
			t.htmlOpen, t.htmlCloser, t.htmlDone = true, block.htmlCloser, false
			t.htmlTail = t.htmlTail[:0]
			t.paragraph = false
			return lineDecision{kind: lineLiteral}, true
		case leafHeading:
			closeUnmatched()
			commit()
			t.paragraph = false
			return lineDecision{kind: lineInline, prefix: past.pos}, true
		case leafList:
			closeUnmatched()
			markerEnd := past.col + block.markerWidth
			afterMarker := lineCursor{line: c.line, pos: past.pos + block.markerWidth, col: markerEnd}
			content := afterMarker.peekSpace()
			indent := markerEnd + 1
			if spaces := content.col - markerEnd; !content.atEnd() && spaces >= 1 && spaces <= 4 {
				indent = markerEnd + spaces
			}
			containers = append(containers, container{indent: indent})
			matched = len(containers)
			paragraph = false
			c = afterMarker
			c.skipToColumn(indent)
			continue
		}
		wasOpen := paragraph
		if !wasOpen {
			closeUnmatched()
		}
		commit()
		t.paragraph = true
		return lineDecision{kind: lineInline, prefix: past.pos, continuing: wasOpen}, true
	}
}

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
	if match := htmlType6.FindSubmatch(rest); match != nil && strings.Contains(blockTagNames, "|"+strings.ToLower(string(match[1]))+"|") {
		return leafShape{kind: leafHTML}
	}
	if !paragraph && htmlType7.Match(rest) {
		return leafShape{kind: leafHTML}
	}
	return leafShape{}
}

// noteHTMLByte watches a literal line for its HTML block's closer. The block
// ends with that line, whatever follows the closer on it.
func (t *blockTracker) noteHTMLByte(b byte) {
	if !t.htmlOpen || t.htmlCloser == "" || t.htmlDone {
		return
	}
	t.htmlTail = append(t.htmlTail, b)
	if len(t.htmlTail) > len(t.htmlCloser) {
		t.htmlTail = t.htmlTail[1:]
	}
	t.htmlDone = strings.EqualFold(string(t.htmlTail), t.htmlCloser)
}

// endLiteralLine closes an HTML block whose closer appeared on this line.
func (t *blockTracker) endLiteralLine() {
	if t.htmlDone {
		t.htmlOpen, t.htmlDone = false, false
		t.htmlCloser = ""
	}
}

// open reports whether the next line continues a literal block.
func (t *blockTracker) open() bool { return t.fenceLen > 0 || t.htmlOpen }
