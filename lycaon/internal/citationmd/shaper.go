package citationmd

// shaper writes presented bytes and owns the whitespace a removed node leaves.
// Text whitespace waits in pending until a visible byte decides whether it
// survives.
type shaper struct {
	out     []byte
	pending []byte
	// lineStart is where the current line begins inside pending.
	lineStart int
	// lineOut is where the current line begins in out once it has output, and
	// literalEnd is the end of the last literal bytes, which nothing may retract.
	lineOut    int
	literalEnd int
	emitted    bool
	visible    bool
	removed    bool
	dropped    bool
	skipSpace  bool
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
			s.flush()
			s.out = append(s.out, b)
			s.emitted, s.visible, s.dropped, s.skipSpace = true, true, false, false
		}
	}
}

// endLine drops a line that only held removed nodes, and a blank line right after
// one, so that removal leaves no extra paragraph gap. What follows a dropped line
// loses its indent, which would otherwise turn a paragraph line into code.
func (s *shaper) endLine() {
	s.skipSpace = false
	if !s.visible && (s.removed || s.dropped) {
		s.pending = s.pending[:s.lineStart]
		s.dropped = true
		s.skipSpace = true
	} else {
		s.pending = append(s.pending, '\n')
	}
	s.lineStart = len(s.pending)
	s.visible, s.removed = false, false
}

// literal writes bytes verbatim, along with any whitespace before them.
func (s *shaper) literal(data []byte) {
	if len(data) == 0 {
		return
	}
	s.flush()
	s.out = append(s.out, data...)
	s.literalEnd = len(s.out)
	endsLine := data[len(data)-1] == '\n'
	s.emitted, s.visible, s.dropped, s.skipSpace = true, !endsLine, false, false
	s.removed = s.removed && !endsLine
}

// remove records a removed node. trimLeading also takes the horizontal space that
// led up to it, as before sentence punctuation; otherwise that space keeps the
// neighbouring words apart.
func (s *shaper) remove(trimLeading bool) {
	if trimLeading || !s.visible {
		for len(s.pending) > s.lineStart && isSpaceByte(s.pending[len(s.pending)-1]) {
			s.pending = s.pending[:len(s.pending)-1]
		}
	}
	s.removed = true
	s.skipSpace = !s.visible
}

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
	s.lineStart = 0
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

// retractLine takes back the current line's output, which held only container
// markup, and leaves earlier newlines pending so that they can still be trimmed.
func (s *shaper) retractLine() {
	s.out = s.out[:s.lineOut]
	s.pending = s.pending[:0]
	for len(s.out) > s.literalEnd && isSpaceOrNewline(s.out[len(s.out)-1]) {
		s.pending = append(s.pending, s.out[len(s.out)-1])
		s.out = s.out[:len(s.out)-1]
	}
	for i, j := 0, len(s.pending)-1; i < j; i, j = i+1, j-1 {
		s.pending[i], s.pending[j] = s.pending[j], s.pending[i]
	}
	s.lineStart = len(s.pending)
	s.visible, s.removed = false, true
	s.skipSpace = true
	if len(s.out) == 0 {
		s.emitted = false
	}
}

// close discards trailing whitespace outside literal zones.
func (s *shaper) close() { s.pending = s.pending[:0] }

func (s *shaper) String() string { return string(s.out) }
