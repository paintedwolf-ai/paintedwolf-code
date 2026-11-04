package citationmd

// shaper writes presented bytes and owns the whitespace a removed node leaves.
// Text whitespace waits in pending until a visible byte decides whether it
// survives.
type shaper struct {
	out     []byte
	pending []byte
	// lineStart is where the current line begins inside pending, and breaks counts
	// the line ends before it.
	lineStart int
	breaks    int
	// lineOut is where the current line begins in out once it has output, and
	// literalEnd is the end of the last literal bytes, which nothing may retract.
	lineOut    int
	literalEnd int
	// protected spans of out are code or host-written links; htmlBlocks are HTML
	// blocks, where Markdown escapes mean nothing.
	protected  []Span
	htmlBlocks []Span
	emitted    bool
	visible    bool
	removed    bool
	dropped    bool
	skipSpace  bool
	// restore is the indent of a line emptied by removal, which the next text line
	// takes, so that an old continuation line cannot become indented code.
	restore    []byte
	hasRestore bool
	// escapeNext puts an escape on the next visible byte, which would otherwise
	// open a block now that the node before it is gone.
	escapeNext bool
}

func (s *shaper) text(data []byte) {
	for _, b := range data {
		switch b {
		case '\n':
			s.endLine()
		case ' ', '\t', '\r':
			if !s.skipSpace {
				s.pending = append(s.pending, b)
			}
		default:
			s.visibleByte(b)
		}
	}
}

func (s *shaper) visibleByte(b byte) {
	s.prepareVisible()
	s.out = append(s.out, escapeFor(b, &s.escapeNext)...)
	s.out = append(s.out, b)
	s.emitted, s.visible, s.dropped, s.skipSpace = true, true, false, false
}

// prepareVisible settles what waits before the first visible byte of a line.
func (s *shaper) prepareVisible() {
	if s.hasRestore {
		s.pending = append(s.pending, s.restore...)
		s.hasRestore = false
	}
	s.flush()
}

// escapeFor returns the escape a byte needs, if one is due. An ordered marker's
// digits pass, and the escape lands on the punctuation after them.
func escapeFor(b byte, due *bool) string {
	if !*due || (b >= '0' && b <= '9') {
		return ""
	}
	*due = false
	return `\`
}

// endLine keeps a line that only held removed nodes as one blank line, so that the
// lines around it do not merge, and collapses any run of blank lines it would add.
func (s *shaper) endLine() {
	s.skipSpace = false
	s.escapeNext = false
	switch {
	case !s.visible && (s.removed || s.dropped):
		s.pending = s.pending[:s.lineStart]
		if s.blankBefore() {
			s.dropped = true
		} else {
			s.pending = append(s.pending, '\n')
			s.breaks++
			s.dropped = true
		}
		s.skipSpace = true
		s.hasRestore = s.removed || s.hasRestore
	default:
		s.pending = append(s.pending, '\n')
		s.breaks++
	}
	s.lineStart = len(s.pending)
	s.visible, s.removed = false, false
}

// blankBefore reports that output so far already ends in a blank line, or has not
// started.
func (s *shaper) blankBefore() bool {
	if !s.emitted {
		return true
	}
	breaks := s.breaks
	if len(s.out) > 0 && s.out[len(s.out)-1] == '\n' {
		breaks++
	}
	return breaks >= 2
}

// literal writes bytes verbatim, along with any whitespace before them.
func (s *shaper) literal(data []byte) {
	if len(data) == 0 {
		return
	}
	s.prepareVisible()
	s.out = append(s.out, escapeFor(data[0], &s.escapeNext)...)
	s.out = append(s.out, data...)
	s.literalEnd = len(s.out)
	endsLine := data[len(data)-1] == '\n'
	s.emitted, s.visible, s.dropped, s.skipSpace = true, !endsLine, false, false
	s.removed = s.removed && !endsLine
}

// protect writes bytes verbatim and exempts them from anchor neutralizing.
func (s *shaper) protect(data []byte) {
	if len(data) == 0 {
		return
	}
	s.prepareVisible()
	s.out = append(s.out, escapeFor(data[0], &s.escapeNext)...)
	start := len(s.out)
	s.literal(data)
	s.protected = append(s.protected, Span{start, len(s.out)})
}

// htmlBlock writes an HTML block verbatim and notes where it sits.
func (s *shaper) htmlBlock(data []byte) {
	if len(data) == 0 {
		return
	}
	s.prepareVisible()
	s.out = append(s.out, escapeFor(data[0], &s.escapeNext)...)
	start := len(s.out)
	s.literal(data)
	s.htmlBlocks = append(s.htmlBlocks, Span{start, len(s.out)})
}

// remove records a removed node. trimLeading also takes the horizontal space that
// led up to it, as before sentence punctuation; otherwise that space keeps the
// neighbouring words apart.
func (s *shaper) remove(trimLeading bool) {
	if !s.visible && s.pendingSpace() {
		s.restore = append(s.restore[:0], s.pending[s.lineStart:]...)
	}
	if trimLeading || !s.visible {
		for len(s.pending) > s.lineStart && isSpaceByte(s.pending[len(s.pending)-1]) {
			s.pending = s.pending[:len(s.pending)-1]
		}
	}
	s.removed = true
	s.skipSpace = !s.visible
}

// separate keeps neighbours apart with one space where joining them would create
// syntax.
func (s *shaper) separate() { s.pending = append(s.pending, ' ') }

func (s *shaper) flush() {
	if !s.visible {
		s.lineOut = len(s.out)
		if s.emitted {
			s.lineOut += s.lineStart
		}
	}
	if s.emitted {
		s.out = append(s.out, s.pending...)
	}
	s.pending = s.pending[:0]
	s.lineStart, s.breaks = 0, 0
}

// leftNeighbour is the last visible text byte on the current line, or zero when
// nothing retractable stands there: no text, code, a kept link, or an escaped byte.
func (s *shaper) leftNeighbour() byte {
	at := len(s.out) - 1
	if !s.visible || at < s.literalEnd || at < 0 || (at > 0 && s.out[at-1] == '\\') {
		return 0
	}
	// `](` could become a link's destination once its contents go.
	if s.out[at] == '(' && at > 0 && s.out[at-1] == ']' {
		return 0
	}
	return s.out[at]
}

// insideLinkDestination reports an open `](` before the current text on its line:
// closing up what follows it could make a live link from text that failed to be one.
func (s *shaper) insideLinkDestination() bool {
	lowest := max(s.lineOut, len(s.out)-MaxInlineProtocolNodeBytes)
	for at := len(s.out) - 1; at > lowest; at-- {
		switch s.out[at] {
		case ')':
			return false
		case '(':
			return s.out[at-1] == ']'
		}
	}
	return false
}

// retractNeighbour takes back the left neighbour and the space around it, leaving
// the space before it pending so that the removal can still decide on it.
func (s *shaper) retractNeighbour() {
	s.out = s.out[:len(s.out)-1]
	s.reclaimSpace()
}

// retractConjunction takes back a conjunction left with nothing to join.
func (s *shaper) retractConjunction() bool {
	for _, word := range []string{" and", " or", " &"} {
		text := string(s.out[max(s.literalEnd, s.lineOut, len(s.out)-len(word)):])
		if text == word && s.pendingSpace() {
			s.out = s.out[:len(s.out)-len(word)]
			s.pending = s.pending[:s.lineStart]
			return true
		}
	}
	return false
}

func (s *shaper) trimPending() { s.pending = s.pending[:s.lineStart] }

func (s *shaper) reclaimSpace() {
	end := len(s.out)
	for end > s.literalEnd && end > s.lineOut && isSpaceByte(s.out[end-1]) {
		end--
	}
	s.pending = append(s.pending[:s.lineStart], s.out[end:]...)
	s.out = s.out[:end]
	if len(s.out) <= s.lineOut {
		s.visible = false
	}
	if len(s.out) == 0 {
		s.emitted = false
	}
}

func (s *shaper) lastByte() byte {
	if len(s.out) == 0 {
		return '\n'
	}
	return s.out[len(s.out)-1]
}

func (s *shaper) pendingSpace() bool { return len(s.pending) > s.lineStart }

// onlyTextOnLine reports that no literal bytes sit on the current line.
func (s *shaper) onlyTextOnLine() bool { return s.literalEnd <= s.lineOut }

// retractLine takes back the current line's output, which held only markup, and
// leaves earlier line ends pending so that they can still be trimmed.
func (s *shaper) retractLine() {
	s.out = s.out[:s.lineOut]
	s.pending = s.pending[:0]
	s.breaks = 0
	for len(s.out) > s.literalEnd && isSpaceOrNewline(s.out[len(s.out)-1]) {
		if s.out[len(s.out)-1] == '\n' {
			s.breaks++
		}
		s.pending = append(s.pending, s.out[len(s.out)-1])
		s.out = s.out[:len(s.out)-1]
	}
	for i, j := 0, len(s.pending)-1; i < j; i, j = i+1, j-1 {
		s.pending[i], s.pending[j] = s.pending[j], s.pending[i]
	}
	s.lineStart = len(s.pending)
	s.visible, s.removed = false, true
	s.skipSpace = true
	s.restore = s.restore[:0]
	if len(s.out) == 0 {
		s.emitted = false
	}
}

// close discards trailing whitespace outside literal zones.
func (s *shaper) close() { s.pending = s.pending[:0] }
