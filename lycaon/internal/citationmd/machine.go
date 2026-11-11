package citationmd

type machineState int

const (
	machineText machineState = iota
	machineNode
	machineSkipLine
)

// nodeMachine applies the scanner to text with Markdown's escape rule and the
// newline and overflow recovery boundaries. pos is the next byte to offer: a
// released bracket moves it back to just past the '[', so that any zone or node
// its contents passed over is still seen.
type nodeMachine struct {
	source  string
	sink    *documentBuilder
	opts    Options
	scanner nodeScanner
	state   machineState
	escaped bool
	pos     int
	start   int
	// skipPrefix keeps the bounded start of an overflowed node for its issue.
	skipPrefix string
	skipKind   NodeKind
	// lineLimit is the end of the line holding pos, measured once per line.
	lineLimit int
}

func newNodeMachine(source string, opts Options, sink *documentBuilder) *nodeMachine {
	return &nodeMachine{source: source, sink: sink, opts: opts}
}

// idle reports that no node is open, so a literal zone may begin here.
func (m *nodeMachine) idle() bool { return m.state == machineText }

// skipTo moves past bytes the machine must not see.
func (m *nodeMachine) skipTo(pos int) {
	m.pos = pos
	m.escaped = false
}

// feed offers the byte at pos and moves pos to the next byte to offer.
func (m *nodeMachine) feed(b byte) {
	switch m.state {
	case machineSkipLine:
		if b == '\n' {
			m.finishSkip()
			m.state = machineText
			return
		}
		m.pos++
	case machineNode:
		m.feedNode(b)
	default:
		m.feedText(b)
	}
}

func (m *nodeMachine) feedText(b byte) {
	if b == '[' && !m.escaped {
		m.state, m.start = machineNode, m.pos
		m.scanner.begin(m.nodeWindow(), m.precededByIdentifier(), m.opts)
		m.pos++
		return
	}
	m.escaped = !m.escaped && b == '\\'
	m.pos++
}

// nodeWindow is the source from the open bracket, cut at the line and the cap.
func (m *nodeMachine) nodeWindow() string {
	if m.pos >= m.lineLimit {
		m.lineLimit = lineEnd(m.source, m.pos)
	}
	return m.source[m.pos:min(m.lineLimit, m.pos+MaxInlineProtocolNodeBytes)]
}

func (m *nodeMachine) precededByIdentifier() bool {
	if m.pos == 0 {
		return false
	}
	c := m.source[m.pos-1]
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func (m *nodeMachine) feedNode(b byte) {
	switch m.scanner.feed(b) {
	case scanClosed:
		m.pos++
		m.closeNode()
	case scanRelease:
		m.release()
	case scanUnterminated:
		m.damage(IssueUnterminatedNode)
		m.state = machineText
	case scanOverflow:
		m.skipPrefix = boundedToken(string(m.scanner.buf))
		m.skipKind = m.scanner.kind()
		m.state = machineSkipLine
		m.pos++
	default:
		m.pos++
	}
}

func (m *nodeMachine) release() {
	m.state = machineText
	m.escaped = false
	m.pos = m.start + 1
}

func (m *nodeMachine) closeNode() {
	token := m.scanner.buf
	node := parseNode(string(token), m.scanner.mode, m.scanner.confirmed || m.scanner.attempt, m.scanner.indexed, m.opts)
	switch {
	case node.prose:
		m.release()
	case node.issue != "":
		m.state = machineText
		m.escaped = false
		m.sink.damaged(Issue{Code: node.issue, Kind: node.kind, Span: Span{m.start, m.pos}, Token: boundedToken(string(token))})
	default:
		m.state = machineText
		m.escaped = false
		m.sink.closed(token, node, m.start)
	}
}

func (m *nodeMachine) damage(code IssueCode) {
	end := m.pos
	if end > m.start && m.source[end-1] == '\r' {
		end--
	}
	m.sink.damaged(Issue{Code: code, Kind: m.scanner.kind(), Span: Span{m.start, end}, Token: boundedToken(m.source[m.start:end])})
}

func (m *nodeMachine) finishSkip() {
	end := m.pos
	if end > m.start && m.source[end-1] == '\r' {
		end--
	}
	m.sink.damaged(Issue{Code: IssueNodeTooLong, Kind: m.skipKind, Span: Span{m.start, end}, Token: m.skipPrefix})
}

// finish applies the end-of-input boundary, which matches a newline's. It reports
// whether a released bracket needs its contents scanned again.
func (m *nodeMachine) finish() bool {
	switch m.state {
	case machineSkipLine:
		m.finishSkip()
	case machineNode:
		if !m.scanner.confirmed {
			m.release()
			return true
		}
		m.damage(IssueUnterminatedNode)
	case machineText:
	}
	m.state = machineText
	return false
}

// kind names a node's family for its issue.
func (s *nodeScanner) kind() NodeKind {
	switch s.mode {
	case modeVerify:
		return KindVerify
	case modeArtifact:
		return KindArtifact
	case modeRepair:
		return KindRepair
	default:
		return KindCitation
	}
}
