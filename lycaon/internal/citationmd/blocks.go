package citationmd

import (
	"bytes"
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
		case leafParagraph:
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
