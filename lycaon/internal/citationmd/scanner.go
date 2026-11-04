package citationmd

import (
	"regexp"
	"strings"
)

// scanResult is the scanner's decision after one byte.
type scanResult int

const (
	// scanPending consumed the byte; the node is still open.
	scanPending scanResult = iota
	// scanRelease rejected the bracket as a node. It is ordinary text.
	scanRelease
	// scanClosed consumed the closing delimiter.
	scanClosed
	// scanUnterminated met a newline inside a confirmed node, not consumed.
	scanUnterminated
	// scanOverflow would exceed the node cap; the byte was not consumed.
	scanOverflow
)

// bodyMode selects a node's closing rule.
type bodyMode int

const (
	modeLexeme bodyMode = iota
	modeCitation
	modeVerify
	modeArtifact
	modeRepair
)

var (
	// reservedHead names the directive a bracket reaches for, spaced or not.
	reservedHead = regexp.MustCompile(`^\[(verify|artifact)[ \t]*:`)
	// repairHead is the only reserved cite form; `[cite: Smith 2020]` is prose.
	repairHead = regexp.MustCompile(`^\[cite[ \t]*:[ \t]*\S+[ \t]*=>`)
	// namespacedHandleHead is a handle whose namespace equals a reserved word.
	namespacedHandleHead = regexp.MustCompile(`^\[(?:verify|artifact|cite):([a-z_]+)#[1-9][0-9]*(?:[\]:, \t]|$)`)
	// malformedCoordinate is a path clearly reaching for a coordinate.
	malformedCoordinate = regexp.MustCompile(`^[^:#]*[./][^:#]*(?::[0-9]+:[0-9].*|:L[0-9].*|:[0-9]+-$|:[0-9]+-[^0-9].*|#L[0-9]*(?:-L?[0-9]*)?)$`)
)

// nodeScanner recognizes one custom node from its '[', one byte at a time.
type nodeScanner struct {
	opts   Options
	window string
	buf    []byte
	mode   bodyMode
	// indexed means an identifier character precedes the bracket, as in
	// `array[i:2]`, so a path form inside it is an index expression.
	indexed bool
	// confirmed means this cannot be ordinary prose any longer.
	confirmed bool
	// attempt means a member so far is a citation attempt, though words after it
	// could still make the whole bracket prose.
	attempt     bool
	memberStart int
	afterMember bool
	afterQuote  bool
	inQuote     bool
	escaped     bool
	inAngle     bool
	depth       int
	skipUntil   int
}

// begin opens a node at '['. window is the source from there, cut at the line's
// end and the node cap.
func (s *nodeScanner) begin(window string, indexed bool, opts Options) {
	*s = nodeScanner{opts: opts, window: window, buf: append(s.buf[:0], '['), indexed: indexed, memberStart: 1}
	if match := namespacedHandleHead.FindStringSubmatch(window); match != nil && opts.knownKind(match[1]) {
		return
	}
	if match := reservedHead.FindStringSubmatch(window); match != nil {
		s.confirmed = true
		s.mode = modeVerify
		if match[1] == "artifact" {
			s.mode = modeArtifact
		}
		return
	}
	if repairHead.MatchString(window) {
		s.confirmed = true
		s.mode = modeRepair
	}
}

// feed advances the scanner by one byte.
func (s *nodeScanner) feed(b byte) scanResult {
	if b == '\n' {
		if s.confirmed {
			return scanUnterminated
		}
		return scanRelease
	}
	if len(s.buf) >= MaxInlineProtocolNodeBytes {
		if s.confirmed {
			return scanOverflow
		}
		return scanRelease
	}
	if s.mode == modeLexeme {
		return s.feedLexeme(b)
	}
	return s.feedConfirmed(b)
}

// feedLexeme walks an unconfirmed citation group, where each byte may disqualify
// it. A group is decided as a group: no single member settles it unless it is a
// handle of a registered kind.
func (s *nodeScanner) feedLexeme(b byte) scanResult {
	switch {
	case s.inQuote:
		s.feedUnconfirmedQuote(b)
		return scanPending
	case s.afterQuote:
		return s.feedAfterQuote(b)
	case s.afterMember:
		return s.feedAfterMember(b)
	}
	switch b {
	case '"':
		if len(s.buf) != s.memberStart {
			return scanRelease
		}
		s.inQuote = true
	case '#':
		member := string(s.buf[s.memberStart:])
		if s.opts.knownKind(member[strings.LastIndexByte(member, ':')+1:]) {
			return s.confirm(modeCitation, b)
		}
	case ']', ',', ' ', '\t', '\r':
		return s.endMember(b)
	default:
		if !isPathByte(b) {
			return scanRelease
		}
	}
	s.buf = append(s.buf, b)
	return scanPending
}

func (s *nodeScanner) feedUnconfirmedQuote(b byte) {
	s.buf = append(s.buf, b)
	switch {
	case s.escaped:
		s.escaped = false
	case b == '\\':
		s.escaped = true
	case b == '"':
		s.inQuote, s.afterQuote = false, true
	}
}

// feedAfterQuote takes what may follow a string. A colon with a number is a
// coordinate and makes this an attempt; `["key": 3]` stays prose.
func (s *nodeScanner) feedAfterQuote(b byte) scanResult {
	s.afterQuote = false
	if b == ':' {
		next := len(s.buf) + 1
		if s.indexed || next >= len(s.window) || s.window[next] < '0' || s.window[next] > '9' {
			return scanRelease
		}
		return s.confirm(modeCitation, b)
	}
	return s.feedAfterMember(b)
}

// feedAfterMember allows only a quoted excerpt, a separator or the close after a
// complete member, so that bracketed prose releases at its first word.
func (s *nodeScanner) feedAfterMember(b byte) scanResult {
	switch b {
	case ' ', '\t', '\r':
		s.buf = append(s.buf, b)
		s.afterMember = true
		return scanPending
	case '"':
		s.afterMember = false
		if s.attempt {
			return s.confirm(modeCitation, b)
		}
		s.inQuote = true
		s.buf = append(s.buf, b)
		return scanPending
	case ',':
		return s.nextMember(b)
	case ']':
		s.buf = append(s.buf, b)
		return scanClosed
	default:
		return scanRelease
	}
}

func (s *nodeScanner) endMember(b byte) scanResult {
	member := string(s.buf[s.memberStart:])
	if member == "" {
		if (b == ' ' || b == '\t') && s.memberStart > 1 {
			s.buf = append(s.buf, b)
			s.memberStart = len(s.buf)
			return scanPending
		}
		return scanRelease
	}
	attempt, plausible := judgeMember(member, s.indexed, s.opts)
	if !plausible {
		return scanRelease
	}
	s.attempt = s.attempt || attempt
	switch b {
	case ']':
		s.buf = append(s.buf, b)
		return scanClosed
	case ',':
		return s.nextMember(b)
	default:
		s.buf = append(s.buf, b)
		s.afterMember = true
		return scanPending
	}
}

// nextMember passes a comma. An attempt so far settles the group, so a later bad
// member is a defect rather than a reason to call it all prose.
func (s *nodeScanner) nextMember(b byte) scanResult {
	if s.attempt {
		return s.confirm(modeCitation, b)
	}
	s.afterMember = false
	s.buf = append(s.buf, b)
	s.memberStart = len(s.buf)
	return scanPending
}

// judgeMember classifies a bare member. plausible false means no citation can
// contain it, so the bracket is prose.
func judgeMember(member string, indexed bool, opts Options) (attempt, plausible bool) {
	if isBareURL(member) {
		return false, false
	}
	if !indexed && malformedCoordinate.MatchString(member) {
		return true, true
	}
	if strings.Contains(member, "#") {
		return false, handleShaped.MatchString(member)
	}
	path, start, _, ok := splitCoordinate(member)
	if !ok || !validBarePath(path) {
		return false, false
	}
	return isCitationAttempt(path, start, indexed, opts), true
}

var handleShaped = regexp.MustCompile(`^[a-zA-Z0-9_:-]+#[a-zA-Z0-9_-]*$`)

func isBareURL(member string) bool {
	lower := strings.ToLower(member)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "ftp://") || strings.HasPrefix(lower, "www.")
}

// isCitationAttempt decides a bare path by structure. A path with `/` or `.` and a
// coordinate is an attempt unless it is only digits and dots, as in `[1.5:2]`.
// Anything else needs an observed path. Inside an index expression none count.
func isCitationAttempt(path, start string, indexed bool, opts Options) bool {
	if indexed || path == "." || path == "./" {
		return false
	}
	if start != "" && strings.ContainsAny(path, "./") && strings.Trim(path, "0123456789.") != "" {
		return true
	}
	return opts.known(path)
}

func (s *nodeScanner) confirm(mode bodyMode, b byte) scanResult {
	s.mode = mode
	s.confirmed = true
	s.inQuote = b == '"'
	s.afterMember, s.afterQuote = false, false
	s.buf = append(s.buf, b)
	return scanPending
}

// feedConfirmed finds the closing delimiter under the mode's rule.
func (s *nodeScanner) feedConfirmed(b byte) scanResult {
	s.buf = append(s.buf, b)
	if len(s.buf) <= s.skipUntil {
		return scanPending
	}
	if s.escaped {
		s.escaped = false
		return scanPending
	}
	if b == '\\' {
		s.escaped = true
		return scanPending
	}
	switch s.mode {
	case modeVerify:
		// A reason is prose, so its quotes mean nothing, but brackets nest and a
		// closed code span is opaque.
		switch b {
		case '`':
			s.skipCodeSpan()
		case '[':
			s.depth++
		case ']':
			if s.depth == 0 {
				return scanClosed
			}
			s.depth--
		}
	case modeArtifact:
		if b == ']' {
			return scanClosed
		}
	default:
		return s.feedQuoteAware(b)
	}
	return scanPending
}

// skipCodeSpan looks ahead for the closer of the backtick run just opened.
func (s *nodeScanner) skipCodeSpan() {
	if len(s.buf) <= s.skipUntil {
		return
	}
	start := len(s.buf) - 1
	run := start
	for run < len(s.window) && s.window[run] == '`' {
		run++
	}
	length := run - start
	for at := run; at < len(s.window); at++ {
		if s.window[at] != '`' {
			continue
		}
		end := at
		for end < len(s.window) && s.window[end] == '`' {
			end++
		}
		if end-at == length {
			s.skipUntil = end
			return
		}
		at = end
	}
	s.skipUntil = run
}

func (s *nodeScanner) feedQuoteAware(b byte) scanResult {
	switch {
	case s.inQuote:
		s.inQuote = b != '"'
	case s.inAngle:
		s.inAngle = b != '>'
	case b == '"':
		s.inQuote = true
	case b == '<' && s.mode == modeRepair:
		s.inAngle = true
	case b == ']':
		return scanClosed
	}
	return scanPending
}

// isPathByte accepts unquoted path bytes. Bytes of multi-byte runes pass here
// and are checked as runes once the member is complete.
func isPathByte(b byte) bool {
	switch {
	case b >= 0x80, b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	return strings.IndexByte("-_.@+~%=/:", b) >= 0
}
