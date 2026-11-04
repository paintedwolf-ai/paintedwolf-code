package citationmd

import (
	"bytes"
	"regexp"
	"strings"
	"unicode"
)

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
	label []byte
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
		return s.codeHolding
	case modeRawLiteral:
		return s.rawHolding
	default:
		return true
	}
}

func (s *inlineState) setPendingNode(token []byte, node parsedNode, start int) {
	s.pending = &pendingNode{token: append([]byte(nil), token...), node: node, start: start, definition: s.defEligible}
	s.defEligible = false
	s.mode = modeAfterNode
}

func (s *inlineState) trackBrackets(text []byte) {
	for _, b := range text {
		switch {
		case s.textEscaped:
			s.textEscaped = false
		case b == '\\':
			s.textEscaped = true
		case b == '[':
			s.openBracket++
			s.label = s.label[:0]
		case b == ']' && s.openBracket > 0:
			s.openBracket--
			s.afterClose = true
		case len(s.label) < maxLabelBytes:
			s.label = append(s.label, b)
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
	if s.afterClose {
		s.afterClose = false
		if b == '(' && p.canHold(holdDestination) {
			p.beginHold(modeDestination, b)
			s.dest = destinationScanner{}
			return true
		}
		if b == ':' && s.defEligible {
			p.beginHold(modeDefinition, b)
			return true
		}
		s.defEligible = false
	}
	if s.openBracket == 0 && b != ' ' && b != '\t' && b != '[' {
		s.defEligible = false
	}
	if m.escaped {
		return m.feed(b)
	}
	if s.url.note(b) && s.openBracket == 0 {
		p.emitRaw(b)
		return true
	}
	switch {
	case b == '`' && p.canHold(holdCode):
		s.mode, s.codeLen = modeCodeOpen, 0
		return false
	case b == '<' && p.canHold(holdAngle):
		p.beginHold(modeAngle, b)
		return true
	case b == '[' && s.openBracket > 0 && p.canHold(holdLabel):
		s.labelDepth = s.openBracket + 1
		p.beginHold(modeLabel, b)
		return true
	}
	return m.feed(b)
}

func (p *Projector) afterNodeByte(b byte) bool {
	s := &p.inline
	pending := s.pending
	switch {
	case b == '(' && p.canHold(holdDestination):
		s.mode, s.dest = modeDestination, destinationScanner{}
	case b == '[' && p.canHold(holdDestination):
		s.mode = modeReference
	case b == ':' && pending.definition:
		s.mode = modeDefinition
	default:
		s.pending, s.mode = nil, modeText
		p.machine.skipTo(p.at)
		p.commitNode(pending)
		s.afterClose = false
		return false
	}
	s.held = append(s.held[:0], b)
	s.holdStart = p.at
	return true
}

func (p *Projector) codeOpenByte(b byte) bool {
	s := &p.inline
	if b == '`' {
		s.codeLen++
		p.emitRaw(b)
		return true
	}
	s.mode, s.codeRun, s.codeHolding = modeCode, 0, false
	return false
}

// codeByte passes a code span's text through until `[` appears, whose meaning
// depends on whether the span ever closes.
func (p *Projector) codeByte(b byte) bool {
	s := &p.inline
	if b != '`' && s.codeRun == s.codeLen {
		p.closeCode()
		return false
	}
	if b == '`' {
		s.codeRun++
	} else {
		s.codeRun = 0
	}
	if !s.codeHolding && b == '[' {
		s.codeHolding = true
		s.held = s.held[:0]
		s.holdStart = p.at
	}
	if !s.codeHolding {
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
	if s.codeHolding {
		p.out.text(s.held)
	}
	s.mode, s.codeHolding = modeText, false
	p.machine.skipTo(p.at)
}

// failHold replays held bytes as ordinary input, past which this kind of hold
// may start again.
func (p *Projector) failHold(kind holdKind) {
	s := &p.inline
	held := append([]byte(nil), s.held...)
	start := s.holdStart
	s.floors[kind] = start + len(held)
	s.mode, s.codeHolding, s.rawHolding = modeText, false, false
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
	s.openBracket = 0
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

var (
	autolinkForm = regexp.MustCompile(`^<[A-Za-z][A-Za-z0-9.+-]{1,31}:[^\s<>]*>$`)
	emailForm    = regexp.MustCompile(`^<[a-zA-Z0-9.!#$%&'*+/=?^_{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?)*>$`)
	openTagForm  = regexp.MustCompile(`^<[A-Za-z][A-Za-z0-9-]*` + attrSource + `*[ \t]*/?>$`)
	closeTagForm = regexp.MustCompile(`^</[A-Za-z][A-Za-z0-9-]*[ \t]*>$`)
)

// rawLiteralOpeners are the raw HTML forms that may run across lines.
var rawLiteralOpeners = []struct{ opener, closer string }{
	{"<!--", "-->"}, {"<![CDATA[", "]]>"}, {"<?", "?>"},
}

// angleByte decides what a `<` opens: raw HTML, an autolink, or plain text.
func (p *Projector) angleByte(b byte) bool {
	s := &p.inline
	if b == '\n' || len(s.held) >= MaxInlineProtocolNodeBytes {
		p.failHold(holdAngle)
		return false
	}
	if len(s.held) == 1 && !(b == '!' || b == '?' || b == '/' || isASCIILetter(b)) {
		p.failHold(holdAngle)
		return false
	}
	if b == '<' {
		p.failHold(holdAngle)
		return false
	}
	s.held = append(s.held, b)
	text := string(s.held)
	if text[1] == '!' || text[1] == '?' {
		return p.angleDeclaration(text)
	}
	if b != '>' {
		return true
	}
	if autolinkForm.MatchString(text) || emailForm.MatchString(text) || openTagForm.MatchString(text) || closeTagForm.MatchString(text) {
		p.releaseHold()
		return true
	}
	s.held = s.held[:len(s.held)-1]
	p.failHold(holdAngle)
	return false
}

func (p *Projector) angleDeclaration(text string) bool {
	s := &p.inline
	for _, form := range rawLiteralOpeners {
		if text == form.opener {
			p.enterRawLiteral(form.closer)
			return true
		}
		if strings.HasPrefix(form.opener, text) {
			return true
		}
	}
	if len(text) >= 3 && text[2] >= 'A' && text[2] <= 'Z' {
		p.enterRawLiteral(">")
		return true
	}
	last := s.held[len(s.held)-1]
	s.held = s.held[:len(s.held)-1]
	p.failHold(holdAngle)
	return p.inlineStep(last)
}

func (p *Projector) enterRawLiteral(closer string) {
	s := &p.inline
	p.out.text(s.held)
	s.held = s.held[:0]
	s.mode, s.rawCloser, s.rawHolding = modeRawLiteral, closer, false
	s.rawTail = s.rawTail[:0]
	p.machine.skipTo(p.at + 1)
}

// rawLiteralByte is codeByte's counterpart for a string closer.
func (p *Projector) rawLiteralByte(b byte) bool {
	s := &p.inline
	s.rawTail = append(s.rawTail, b)
	if len(s.rawTail) > len(s.rawCloser) {
		s.rawTail = s.rawTail[1:]
	}
	if !s.rawHolding && b == '[' {
		s.rawHolding = true
		s.held = s.held[:0]
		s.holdStart = p.at
	}
	if s.rawHolding {
		s.held = append(s.held, b)
	} else {
		p.emitRaw(b)
	}
	if string(s.rawTail) == s.rawCloser {
		if s.rawHolding {
			p.out.text(s.held)
		}
		s.mode, s.rawHolding = modeText, false
		p.machine.skipTo(p.at + 1)
		return true
	}
	if s.rawHolding && len(s.held) >= MaxInlineProtocolNodeBytes {
		p.failHold(holdAngle)
	}
	return true
}

func (p *Projector) destinationByte(b byte) bool {
	s := &p.inline
	if len(s.held) >= MaxInlineProtocolNodeBytes {
		p.failNodeHold(holdDestination)
		return false
	}
	switch s.dest.step(b, len(s.held) == 1) {
	case destinationValid:
		s.held = append(s.held, b)
		p.releaseHold()
		return true
	case destinationInvalid:
		p.failNodeHold(holdDestination)
		return false
	}
	s.held = append(s.held, b)
	return true
}

func (p *Projector) referenceByte(b byte) bool {
	s := &p.inline
	if b == '\n' || len(s.held) >= MaxInlineProtocolNodeBytes {
		p.failNodeHold(holdDestination)
		return false
	}
	s.held = append(s.held, b)
	if b != ']' {
		return true
	}
	label := string(s.held[1 : len(s.held)-1])
	if label == "" {
		label = string(s.pending.token[1 : len(s.pending.token)-1])
	}
	if p.defs[normalizeLabel(label)] {
		p.releaseHold()
		return true
	}
	s.held = s.held[:len(s.held)-1]
	p.failNodeHold(holdDestination)
	return false
}

var definitionTail = regexp.MustCompile(`^:\s*(?:<[^<>\n]*>|[^\s<]\S*)(?:[ \t]+(?:"[^"\n]*"|'[^'\n]*'|\([^()\n]*\)))?[ \t]*$`)

// definitionByte holds a possible definition to its line's end. A destination
// may wait on the next line, so a colon with nothing after it holds on.
func (p *Projector) definitionByte(b byte) bool {
	s := &p.inline
	full := len(s.held) >= MaxInlineProtocolNodeBytes
	if !full && (b != '\n' || len(bytes.TrimSpace(s.held[1:])) == 0) {
		s.held = append(s.held, b)
		return true
	}
	if !definitionTail.Match(s.held) {
		p.failNodeHold(holdDestination)
		return false
	}
	label := s.label
	if s.pending != nil {
		label = s.pending.token[1 : len(s.pending.token)-1]
	}
	p.defs[normalizeLabel(string(label))] = true
	p.blocks.paragraph = false
	saved := p.at
	p.at--
	p.releaseHold()
	p.at = saved
	return false
}

func (p *Projector) labelByte(b byte) bool {
	s := &p.inline
	if b == '\n' || len(s.held) >= MaxInlineProtocolNodeBytes {
		p.failHold(holdLabel)
		return false
	}
	s.held = append(s.held, b)
	switch b {
	case '[':
		s.labelDepth++
	case ']':
		s.labelDepth--
		if s.labelDepth == 0 {
			s.mode = modeLabelEnd
		}
	}
	return true
}

func (p *Projector) labelEndByte(b byte) bool {
	s := &p.inline
	if b != '(' {
		p.failHold(holdLabel)
		return false
	}
	s.held = append(s.held, b)
	s.mode, s.dest = modeDestination, destinationScanner{}
	return true
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
	s.openBracket, s.afterClose, s.textEscaped = 0, false, false
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
		if s.codeRun == s.codeLen {
			p.closeCode()
		} else {
			p.failHold(holdCode)
		}
	case modeAngle, modeRawLiteral:
		p.failHold(holdAngle)
	case modeLabel, modeLabelEnd:
		p.failHold(holdLabel)
	default:
		p.failNodeHold(holdDestination)
	}
}

func isASCIILetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

// normalizeLabel applies CommonMark's label matching.
func normalizeLabel(label string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.Map(unicode.ToLower, label))), " ")
}

type destinationResult int

const (
	destinationPending destinationResult = iota
	destinationValid
	destinationInvalid
)

// destinationScanner validates `(dest "title")` after a link label's `]`.
type destinationScanner struct {
	stage   int
	depth   int
	escaped bool
	closer  byte
}

const (
	destLead = iota
	destBare
	destAngle
	destAfter
	destTitle
	destEnd
)

// step advances past the opening `(`, which the caller passes with first set.
func (d *destinationScanner) step(b byte, first bool) destinationResult {
	if b == '\n' {
		return destinationInvalid
	}
	if d.escaped {
		d.escaped = false
		return destinationPending
	}
	switch d.stage {
	case destLead:
		switch {
		case isSpaceByte(b):
		case b == ')':
			return destinationValid
		case b == '<':
			d.stage = destAngle
		default:
			d.stage = destBare
			return d.bare(b)
		}
	case destBare:
		return d.bare(b)
	case destAngle:
		switch b {
		case '\\':
			d.escaped = true
		case '>':
			d.stage = destAfter
		case '<':
			return destinationInvalid
		}
	case destAfter:
		return d.after(b)
	case destTitle:
		if b == '\\' {
			d.escaped = true
		} else if b == d.closer {
			d.stage = destEnd
		}
	case destEnd:
		if b == ')' {
			return destinationValid
		}
		if !isSpaceByte(b) {
			return destinationInvalid
		}
	}
	return destinationPending
}

func (d *destinationScanner) bare(b byte) destinationResult {
	switch {
	case b == '\\':
		d.escaped = true
	case isSpaceByte(b):
		d.stage = destAfter
	case b == '(':
		d.depth++
	case b == ')':
		if d.depth == 0 {
			return destinationValid
		}
		d.depth--
	case b < 0x20:
		return destinationInvalid
	}
	return destinationPending
}

func (d *destinationScanner) after(b byte) destinationResult {
	switch {
	case isSpaceByte(b):
	case b == ')':
		return destinationValid
	case b == '"' || b == '\'':
		d.stage, d.closer = destTitle, b
	case b == '(':
		d.stage, d.closer = destTitle, ')'
	default:
		return destinationInvalid
	}
	return destinationPending
}

// urlTracker follows GFM's bare URL autolink shape, whose path may hold
// brackets that must stay part of the URL.
type urlTracker struct {
	word  []byte
	stage int
}

const (
	urlNone = iota
	urlDomain
	urlDomainDot
	urlPath
)

// note observes one text byte and reports whether it lies in a URL's path.
func (u *urlTracker) note(b byte) bool {
	if b == ' ' || b == '\t' || b == '\n' || b == '<' {
		*u = urlTracker{word: u.word[:0]}
		return false
	}
	if u.stage == urlPath {
		return true
	}
	if b == '*' || b == '_' || b == '~' || b == '(' {
		*u = urlTracker{word: u.word[:0]}
		return false
	}
	if u.stage == urlDomain && b == '.' {
		u.stage = urlDomainDot
	}
	if u.stage == urlDomainDot && (b == '/' || b == '#' || b == '?') {
		u.stage = urlPath
		return false
	}
	if u.stage == urlNone && len(u.word) < 8 {
		u.word = append(u.word, b)
		for _, prefix := range []string{"http://", "https://", "ftp://", "www."} {
			if string(u.word) == prefix {
				u.stage = urlDomain
			}
		}
	}
	return false
}
