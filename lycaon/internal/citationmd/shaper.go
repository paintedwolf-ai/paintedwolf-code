package citationmd

// shaper writes presented bytes and owns the whitespace a removed node leaves.
// Parsed presentation and streaming both write through it, which is what makes
// their results byte-identical. Text whitespace waits in pending until a
// visible byte decides whether it survives.
type shaper struct {
	out     []byte
	pending []byte
	// lineStart is where the current line begins inside pending.
	lineStart int
	emitted   bool
	visible   bool
	removed   bool
	dropped   bool
	skipSpace bool
}

func (s *shaper) text(data []byte) {
	for _, b := range data {
		switch b {
		case '\n':
			s.endLine()
		case ' ', '\t':
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

// endLine drops a line that only held removed nodes, and a blank line right
// after one, so that removal leaves no extra paragraph gap.
func (s *shaper) endLine() {
	s.skipSpace = false
	if !s.visible && (s.removed || s.dropped) {
		s.pending = s.pending[:s.lineStart]
		s.dropped = true
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
	endsLine := data[len(data)-1] == '\n'
	s.emitted, s.visible, s.dropped, s.skipSpace = true, !endsLine, false, false
	s.removed = s.removed && !endsLine
}

// remove records a removed node and the horizontal space that led up to it.
func (s *shaper) remove() {
	for len(s.pending) > s.lineStart && (s.pending[len(s.pending)-1] == ' ' || s.pending[len(s.pending)-1] == '\t') {
		s.pending = s.pending[:len(s.pending)-1]
	}
	s.removed = true
	s.skipSpace = !s.visible
}

func (s *shaper) flush() {
	if s.emitted {
		s.out = append(s.out, s.pending...)
	}
	s.pending = s.pending[:0]
	s.lineStart = 0
}

// close discards trailing whitespace outside literal zones.
func (s *shaper) close() { s.pending = s.pending[:0] }

// drain returns the bytes written since the last drain.
func (s *shaper) drain() string {
	out := string(s.out)
	s.out = s.out[:0]
	return out
}
