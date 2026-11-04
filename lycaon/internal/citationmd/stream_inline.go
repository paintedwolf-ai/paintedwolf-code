package citationmd

type inlineMode int

const (
	modeText inlineMode = iota
	modeAfterNode
	modeCodeOpen
	modeCode
	modeAngle
	modeRawLiteral
	modeDestination
	modeReference
	modeDefinition
	modeLabel
	modeLabelEnd
)

// holdKind indexes the per-kind replay floors.
type holdKind int

const (
	holdCode holdKind = iota
	holdAngle
	holdDestination
	holdLabel
	holdKindCount
)

// maxLabelBytes is CommonMark's bound on a link label.
const maxLabelBytes = 999

// pendingNode is a closed node awaiting the byte that settles it.
type pendingNode struct {
	token      []byte
	node       parsedNode
	start      int
	definition bool
}

// inlineState tracks the inline structures a byte stream can only settle later.
// Bytes whose meaning depends on a later byte wait in held; a hold that fails is
// replayed as ordinary input. Floors stop a replay from re-entering the same kind
// of hold, which keeps total work linear in input size.
type inlineState struct {
	mode        inlineMode
	held        []byte
	holdStart   int
	floors      [holdKindCount]int
	pending     *pendingNode
	openBracket int
	afterClose  bool
	textEscaped bool
	defEligible bool
	// label is the text since the last `[`, kept to a definition's label bound.
	label       []byte
	codeLen     int
	codeRun     int
	codeHolding bool
	rawCloser   string
	rawTail     []byte
	rawHolding  bool
	labelDepth  int
	dest        destinationScanner
	url         urlTracker
}

func (s *inlineState) holding() bool {
	switch s.mode {
	case modeText, modeAfterNode, modeCodeOpen:
		return false
	case modeCode:
		return s.code.holding
	case modeRawLiteral:
		return s.raw.holding
	default:
		return true
	}
}

func (s *inlineState) setPendingNode(token []byte, node parsedNode, start int) {
	s.pending = &pendingNode{token: append([]byte(nil), token...), node: node, start: start, definition: s.brackets.defEligible}
	s.brackets.defEligible = false
	s.mode = modeAfterNode
}

func (s *inlineState) trackBrackets(text []byte) {
	for _, b := range text {
		switch {
		case s.brackets.escaped:
			s.brackets.escaped = false
		case b == '\\':
			s.brackets.escaped = true
		case b == '[':
			s.brackets.open++
			s.brackets.label = s.brackets.label[:0]
		case b == ']' && s.brackets.open > 0:
			s.brackets.open--
			s.brackets.afterClose = true
		case len(s.brackets.label) < maxLabelBytes:
			s.brackets.label = append(s.brackets.label, b)
		}
	}
}

// inlineByte offers one byte of inline content until some mode consumes it.
func (p *Projector) inlineByte(b byte) {
	for !p.inlineStep(b) {
	}
}

func (p *Projector) inlineStep(b byte) bool {
	p.work++
	switch p.inline.mode {
	case modeAfterNode:
		return p.afterNodeByte(b)
	case modeCodeOpen:
		return p.codeOpenByte(b)
	case modeCode:
		return p.codeByte(b)
	case modeAngle:
		return p.angleByte(b)
	case modeRawLiteral:
		return p.rawLiteralByte(b)
	case modeDestination:
		return p.destinationByte(b)
	case modeReference:
		return p.referenceByte(b)
	case modeDefinition:
		return p.definitionByte(b)
	case modeLabel:
		return p.labelByte(b)
	case modeLabelEnd:
		return p.labelEndByte(b)
	default:
		return p.textByte(b)
	}
}

func (p *Projector) canHold(kind holdKind) bool { return p.at >= p.inline.floors[kind] }

func (p *Projector) beginHold(mode inlineMode, b byte) {
	s := &p.inline
	s.mode = mode
	s.held = append(s.held[:0], b)
	s.holdStart = p.at
}

// emitRaw shows a byte the machine must not interpret.
func (p *Projector) emitRaw(b byte) {
	p.out.text([]byte{b})
	p.machine.skipTo(p.at + 1)
}

func (p *Projector) textByte(b byte) bool {
	s := &p.inline
	m := p.machine
	if !m.idle() {
		return m.feed(b)
	}
	if s.brackets.afterClose {
		s.brackets.afterClose = false
		if b == '(' && p.canHold(holdDestination) {
			p.beginHold(modeDestination, b)
			s.dest = destinationScanner{}
			return true
		}
		if b == ':' && s.brackets.defEligible {
			p.beginHold(modeDefinition, b)
			return true
		}
		s.brackets.defEligible = false
	}
	if s.brackets.open == 0 && b != ' ' && b != '\t' && b != '[' {
		s.brackets.defEligible = false
	}
	if m.escaped {
		return m.feed(b)
	}
	if s.url.note(b) && s.brackets.open == 0 {
		p.emitRaw(b)
		return true
	}
	switch {
	case b == '`' && p.canHold(holdCode):
		s.mode, s.code.length = modeCodeOpen, 0
		return false
	case b == '<' && p.canHold(holdAngle):
		p.beginHold(modeAngle, b)
		return true
	case b == '[' && s.brackets.open > 0 && p.canHold(holdLabel):
		s.brackets.labelDepth = s.brackets.open + 1
		p.beginHold(modeLabel, b)
		return true
	}
	return m.feed(b)
}

func (p *Projector) codeOpenByte(b byte) bool {
	s := &p.inline
	if b == '`' {
		s.code.length++
		p.emitRaw(b)
		return true
	}
	s.mode, s.code.run, s.code.holding = modeCode, 0, false
	return false
}

// codeByte passes a code span's text through until `[` appears, whose meaning
// depends on whether the span ever closes.
func (p *Projector) codeByte(b byte) bool {
	s := &p.inline
	if b != '`' && s.code.run == s.code.length {
		p.closeCode()
		return false
	}
	if b == '`' {
		s.code.run++
	} else {
		s.code.run = 0
	}
	if !s.code.holding && b == '[' {
		s.code.holding = true
		s.held = s.held[:0]
		s.holdStart = p.at
	}
	if !s.code.holding {
		p.emitRaw(b)
		return true
	}
	s.held = append(s.held, b)
	if len(s.held) >= MaxInlineProtocolNodeBytes {
		p.failHold(holdCode)
	}
	return true
}

func (p *Projector) closeCode() {
	s := &p.inline
	if s.code.holding {
		p.out.text(s.held)
	}
	s.mode, s.code.holding = modeText, false
	p.machine.skipTo(p.at)
}

// failHold replays held bytes as ordinary input, past which this kind of hold
// may start again.
func (p *Projector) failHold(kind holdKind) {
	s := &p.inline
	held := append([]byte(nil), s.held...)
	start := s.holdStart
	s.floors[kind] = start + len(held)
	s.mode, s.code.holding, s.raw.holding = modeText, false, false
	s.held = s.held[:0]
	p.replay(held, start)
}

func (p *Projector) replay(held []byte, start int) {
	saved := p.at
	p.machine.skipTo(start)
	for i, b := range held {
		p.at = start + i
		p.inlineByte(b)
	}
	p.at = saved
}

// releaseHold shows held bytes verbatim once they proved to be standard syntax.
func (p *Projector) releaseHold() {
	s := &p.inline
	if s.pending != nil {
		p.out.text(s.pending.token)
		s.pending = nil
	}
	p.out.text(s.held)
	s.held = s.held[:0]
	s.mode = modeText
	s.brackets.open = 0
	p.machine.skipTo(p.at + 1)
}

// failNodeHold commits the waiting node and replays what followed it.
func (p *Projector) failNodeHold(kind holdKind) {
	s := &p.inline
	if pending := s.pending; pending != nil {
		s.pending = nil
		p.commitNode(pending)
	}
	p.failHold(kind)
}

// boundary ends every structure that cannot cross into a new block.
func (p *Projector) boundary() {
	s := &p.inline
	for s.mode != modeText || !p.machine.idle() {
		if !p.machine.idle() {
			p.machine.finish()
			continue
		}
		p.settleAtBoundary()
	}
	s.brackets.open, s.brackets.afterClose, s.brackets.escaped = 0, false, false
	s.url = urlTracker{}
}

func (p *Projector) settleAtBoundary() {
	s := &p.inline
	switch s.mode {
	case modeAfterNode:
		pending := s.pending
		s.pending, s.mode = nil, modeText
		p.commitNode(pending)
	case modeCodeOpen:
		s.mode = modeText
	case modeCode:
		if s.code.run == s.code.length {
			p.closeCode()
		} else {
			p.failHold(holdCode)
		}
	case modeAngle, modeRawLiteral:
		p.failHold(holdAngle)
	case modeLabel, modeLabelEnd:
		p.failHold(holdLabel)
	case modeDefinition:
		p.settleDefinition()
	default:
		p.failNodeHold(holdDestination)
	}
}
