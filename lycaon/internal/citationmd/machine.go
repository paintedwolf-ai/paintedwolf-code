package citationmd

type machineState int

const (
	machineText machineState = iota
	machineNode
	machineSkipLine
)

// nodeMachine applies the scanner to text with Markdown's escape rule and the
// newline and overflow recovery boundaries.
type nodeMachine struct {
	sink    *documentBuilder
	scanner nodeScanner
	state   machineState
	escaped bool
	pos     int
	start   int
	// skipPrefix keeps the bounded start of an overflowed node for its issue.
	skipPrefix string
	skipKind   NodeKind
}

func newNodeMachine(opts Options, sink *documentBuilder) *nodeMachine {
	return &nodeMachine{sink: sink, scanner: nodeScanner{opts: opts}}
}

// idle reports that no node is open, so a literal zone may begin here.
func (m *nodeMachine) idle() bool { return m.state == machineText }

// skipTo moves past bytes the machine must not see.
func (m *nodeMachine) skipTo(pos int) {
	m.pos = pos
	m.escaped = false
}

// feed offers one byte. False means the byte was not consumed: an open node
// ended before it, and the caller offers it again now that the machine is idle.
func (m *nodeMachine) feed(b byte) bool {
	switch m.state {
	case machineSkipLine:
		if b == '\n' {
			m.finishSkip()
			m.state = machineText
			return false
		}
		m.pos++
		return true
	case machineNode:
		return m.feedNode(b)
	default:
		m.feedText(b)
		return true
	}
}

func (m *nodeMachine) feedText(b byte) {
	if b == '[' && !m.escaped {
		m.scanner.reset()
		m.scanner.buf = append(m.scanner.buf, b)
		m.state, m.start = machineNode, m.pos
		m.pos++
		return
	}
	m.escaped = !m.escaped && b == '\\'
	m.pos++
}

func (m *nodeMachine) feedNode(b byte) bool {
	switch m.scanner.feed(b) {
	case scanClosed:
		m.pos++
		m.closeNode()
	case scanRelease:
		m.state = machineText
		m.escaped = false
		return false
	case scanUnterminated:
		m.sink.damaged(Issue{Code: IssueUnterminatedNode, Kind: m.scanner.kind(), Span: Span{m.start, m.pos}, Token: boundedToken(string(m.scanner.buf))})
		m.state = machineText
		return false
	case scanOverflow:
		m.skipPrefix = boundedToken(string(m.scanner.buf))
		m.skipKind = m.scanner.kind()
		m.state = machineSkipLine
		m.pos++
	default:
		m.pos++
	}
	return true
}

func (m *nodeMachine) closeNode() {
	token := m.scanner.buf
	node := parseNode(string(token), m.scanner.mode, m.scanner.confirmed, m.scanner.opts)
	m.state = machineText
	m.escaped = false
	switch {
	case node.prose:
	case node.issue != "":
		m.sink.damaged(Issue{Code: node.issue, Kind: node.kind, Span: Span{m.start, m.pos}, Token: boundedToken(string(token))})
	default:
		m.sink.closed(token, node, m.start)
	}
}

func (m *nodeMachine) finishSkip() {
	m.sink.damaged(Issue{Code: IssueNodeTooLong, Kind: m.skipKind, Span: Span{m.start, m.pos}, Token: m.skipPrefix})
}

// finish applies the end-of-input boundary, which matches a newline's.
func (m *nodeMachine) finish() {
	switch m.state {
	case machineSkipLine:
		m.finishSkip()
	case machineNode:
		if m.scanner.confirmed {
			m.sink.damaged(Issue{Code: IssueUnterminatedNode, Kind: m.scanner.kind(), Span: Span{m.start, m.pos}, Token: boundedToken(string(m.scanner.buf))})
		}
	case machineText:
	}
	m.state = machineText
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
