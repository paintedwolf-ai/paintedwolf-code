package citationmd

// ProjectionStatus is the host's formatting state for a stream.
type ProjectionStatus struct {
	// Provisional means a malformed or overlong node was met. What is visible
	// is not what acceptance will present until repair or fallback settles it.
	Provisional bool
}

type linePhase int

const (
	phaseHead linePhase = iota
	phaseLiteral
	phaseInline
)

// maxLineHead bounds the bytes held to classify a line.
const maxLineHead = MaxInlineProtocolNodeBytes

// classifyEvery spaces repeat classification of a long undecided line head, so
// holding one costs linear work. Spacing never changes the eventual decision.
const classifyEvery = 256

// Projector turns a partial Markdown response into displayable text. It sees
// bytes in order and decides each from what preceded it, so the split of the
// chunks cannot change the result. Citation acceptance is not its concern; a
// completed stream equals Project over the same source except where a later
// byte changes an earlier structure, as a late link definition can.
type Projector struct {
	opts    Options
	blocks  blockTracker
	machine *nodeMachine
	out     shaper
	issues  []Issue
	status  ProjectionStatus
	at      int
	phase   linePhase
	head    []byte
	inline  inlineState
	defs    map[string]bool
	// work counts inline steps; replay must keep it linear in input size.
	work int
}

// NewProjector returns a projector for one response.
func NewProjector(opts Options) *Projector {
	p := &Projector{opts: opts, defs: map[string]bool{}}
	p.machine = newNodeMachine(opts, p)
	return p
}

// Write consumes a chunk and returns what became visible.
func (p *Projector) Write(chunk string) string {
	for i := 0; i < len(chunk); i++ {
		p.step(chunk[i])
	}
	return p.out.drain()
}

// Close settles the end of input and returns the final visible text.
func (p *Projector) Close() string {
	if p.phase == phaseHead && len(p.head) > 0 {
		p.decideHead(true)
	}
	p.boundary()
	p.machine.finish()
	p.out.close()
	return p.out.drain()
}

// Status reports the formatting state; it is current after every Write.
func (p *Projector) Status() ProjectionStatus { return p.status }

// Issues returns the structured outcomes of damaged nodes seen so far.
func (p *Projector) Issues() []Issue { return p.issues }

func (p *Projector) step(b byte) {
	switch p.phase {
	case phaseHead:
		p.head = append(p.head, b)
		complete := b == '\n' || len(p.head) >= maxLineHead
		if complete || len(p.head) < 64 || len(p.head)%classifyEvery == 0 {
			p.decideHead(complete)
		}
	case phaseLiteral:
		p.literalByte(b)
	default:
		p.inlineLineByte(b)
	}
}

// decideHead classifies the held line head and plays its bytes through.
func (p *Projector) decideHead(complete bool) {
	line := p.head
	if n := len(line); n > 0 && line[n-1] == '\n' {
		line = line[:n-1]
	}
	decision, ok := p.blocks.classify(line, complete)
	if !ok {
		return
	}
	held := p.head
	p.head = nil
	switch decision.kind {
	case lineLiteral:
		p.boundary()
		p.phase = phaseLiteral
		for _, b := range held {
			p.literalByte(b)
		}
	case lineBlank:
		p.boundary()
		p.phase = phaseInline
		p.playInlineLine(held, len(held))
	default:
		if !decision.continuing {
			p.boundary()
			p.inline.brackets.defEligible = true
		}
		p.phase = phaseInline
		p.playInlineLine(held, decision.prefix)
	}
	if p.phase != phaseHead && len(held) > 0 && held[len(held)-1] == '\n' {
		p.phase = phaseHead
	}
}

func (p *Projector) literalByte(b byte) {
	p.out.literal([]byte{b})
	p.blocks.noteHTMLByte(b)
	p.at++
	p.machine.skipTo(p.at)
	if b == '\n' {
		p.blocks.endLiteralLine()
		p.phase = phaseHead
	}
}

// playInlineLine emits a line's container prefix raw, then its content.
func (p *Projector) playInlineLine(held []byte, prefix int) {
	for i, b := range held {
		if i < prefix {
			p.prefixByte(b)
			continue
		}
		p.inlineByte(b)
		p.at++
	}
}

func (p *Projector) inlineLineByte(b byte) {
	p.inlineByte(b)
	p.at++
	if b == '\n' {
		p.phase = phaseHead
	}
}

// prefixByte is container markup: never a node, and part of any open hold.
func (p *Projector) prefixByte(b byte) {
	if p.inline.holding() {
		p.inline.held = append(p.inline.held, b)
	} else {
		p.out.text([]byte{b})
		p.machine.skipTo(p.at + 1)
	}
	p.at++
}

// released implements nodeSink.
func (p *Projector) released(text []byte) {
	p.out.text(text)
	p.inline.trackBrackets(text)
}

// closed implements nodeSink. A closed node waits one byte, because a following
// `(`, `[` or `:` can make it a standard link or a link definition.
func (p *Projector) closed(token []byte, node parsedNode, start int) {
	p.inline.setPendingNode(token, node, start)
}

// damaged implements nodeSink.
func (p *Projector) damaged(issue Issue) {
	p.issues = append(p.issues, issue)
	p.status.Provisional = true
	p.out.remove()
}

// overflowed implements nodeSink.
func (p *Projector) overflowed() {
	p.status.Provisional = true
}

// commitNode presents a node that stayed a protocol node.
func (p *Projector) commitNode(pending *pendingNode) {
	if p.defs[normalizeLabel(string(pending.token[1:len(pending.token)-1]))] {
		p.out.text(pending.token)
		return
	}
	members := make([]Occurrence, 0, len(pending.node.members))
	for _, member := range pending.node.members {
		members = append(members, member.occurrence())
	}
	renderMembers(&p.out, members, projectOccurrence)
}
