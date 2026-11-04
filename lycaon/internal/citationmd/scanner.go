package citationmd

import (
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
)

// handlePrefix confirms a handle once its kind and namespace reach the '#'.
var handlePrefix = regexp.MustCompile(`^` + strings.TrimSuffix(hostmarker.EvidenceHandlePattern, `#[1-9][0-9]*`) + `$`)

// scanResult is the scanner's decision after one byte.
type scanResult int

const (
	// scanPending consumed the byte; the node is still open.
	scanPending scanResult = iota
	// scanRelease rejected the prefix as a node. The buffer is ordinary text
	// and the byte was not consumed.
	scanRelease
	// scanClosed consumed the closing delimiter.
	scanClosed
	// scanUnterminated met a newline inside a confirmed node, not consumed.
	scanUnterminated
	// scanOverflow would exceed the node cap; the byte was not consumed.
	scanOverflow
)

// bodyMode selects a confirmed node's closing rule.
type bodyMode int

const (
	modeLexeme bodyMode = iota
	modeCitation
	modeVerify
	modeArtifact
	modeRepair
)

// nodeScanner recognizes one custom node from its '[', one byte at a time. The
// parser and the streaming projector both drive it, so node limits, malformed
// spans and recovery boundaries cannot differ between them.
type nodeScanner struct {
	opts Options
	buf  []byte
	mode bodyMode
	// confirmed means this cannot be ordinary prose any longer. A bare path
	// stays unconfirmed until it closes, so that prose releases promptly.
	confirmed bool
	// multiSegment records a '/' or '@' path shape, which overflows as a node.
	multiSegment bool
	lexemeStart  int
	// afterLexeme means whitespace followed a bare member.
	afterLexeme bool
	inQuote     bool
	escaped     bool
	inAngle     bool
	depth       int
}

func (s *nodeScanner) reset() {
	s.buf = s.buf[:0]
	s.mode = modeLexeme
	s.confirmed, s.multiSegment, s.afterLexeme = false, false, false
	s.inQuote, s.escaped, s.inAngle = false, false, false
	s.lexemeStart, s.depth = 1, 0
}

// feed advances the scanner by one byte. The caller seeds buf with the '['.
func (s *nodeScanner) feed(b byte) scanResult {
	if b == '\n' {
		if s.confirmed {
			return scanUnterminated
		}
		return scanRelease
	}
	if len(s.buf) >= MaxInlineProtocolNodeBytes {
		if s.confirmed || s.multiSegment {
			return scanOverflow
		}
		return scanRelease
	}
	if s.mode == modeLexeme {
		return s.feedLexeme(b)
	}
	return s.feedConfirmed(b)
}

// feedLexeme handles an unconfirmed node, where each byte may disqualify it.
func (s *nodeScanner) feedLexeme(b byte) scanResult {
	if s.afterLexeme {
		return s.feedAfterLexeme(b)
	}
	switch b {
	case '"':
		if len(s.buf) != s.lexemeStart {
			return scanRelease
		}
		return s.confirm(modeCitation, b)
	case '#':
		if !handlePrefix.Match(s.buf[s.lexemeStart:]) {
			return scanRelease
		}
		return s.confirm(modeCitation, b)
	case ':':
		if s.lexemeStart == 1 && isReservedName(string(s.buf[1:])) {
			return s.confirm(reservedMode(string(s.buf[1:])), b)
		}
	case ']':
		if !s.lexemeAcceptable() {
			return scanRelease
		}
		s.buf = append(s.buf, b)
		return scanClosed
	case ',':
		if !s.lexemeAcceptable() {
			return scanRelease
		}
		s.buf = append(s.buf, b)
		s.lexemeStart = len(s.buf)
		return scanPending
	case ' ', '\t':
		if len(s.buf) == s.lexemeStart && len(s.buf) > 1 {
			s.buf = append(s.buf, b)
			s.lexemeStart = len(s.buf)
			return scanPending
		}
		if !s.lexemeAcceptable() {
			return scanRelease
		}
		s.buf = append(s.buf, b)
		s.afterLexeme = true
		return scanPending
	case '/', '@':
		s.multiSegment = true
	}
	if !isPathByte(b) {
		return scanRelease
	}
	s.buf = append(s.buf, b)
	return scanPending
}

// feedAfterLexeme allows only a quoted excerpt, a separator or the close after
// a bare path member, so that bracketed prose releases at its first word.
func (s *nodeScanner) feedAfterLexeme(b byte) scanResult {
	switch b {
	case ' ', '\t':
		s.buf = append(s.buf, b)
		return scanPending
	case '"':
		s.afterLexeme = false
		return s.confirm(modeCitation, b)
	case ']':
		s.buf = append(s.buf, b)
		return scanClosed
	case ',':
		s.afterLexeme = false
		s.buf = append(s.buf, b)
		s.lexemeStart = len(s.buf)
		return scanPending
	default:
		return scanRelease
	}
}

// lexemeAcceptable decides a bare member at its terminator. Single-segment
// names need a ledger match; structural Markdown forms never qualify.
func (s *nodeScanner) lexemeAcceptable() bool {
	lexeme := string(s.buf[s.lexemeStart:])
	if lexeme == "" || isStructuralBracket(lexeme) {
		return false
	}
	path, _, _, ok := splitCoordinate(lexeme)
	if !ok || !validBarePath(path) {
		return false
	}
	return isMultiSegment(path) || s.opts.known(path)
}

func (s *nodeScanner) confirm(mode bodyMode, b byte) scanResult {
	s.mode = mode
	s.confirmed = true
	if mode == modeCitation && b == '"' {
		s.inQuote = true
	}
	s.buf = append(s.buf, b)
	return scanPending
}

// feedConfirmed finds the closing delimiter under the mode's rule.
func (s *nodeScanner) feedConfirmed(b byte) scanResult {
	s.buf = append(s.buf, b)
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
		// A reason is prose, so its quotes mean nothing but brackets nest.
		switch b {
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

func isReservedName(lexeme string) bool {
	return lexeme == "verify" || lexeme == "artifact" || lexeme == "cite"
}

func reservedMode(lexeme string) bodyMode {
	switch lexeme {
	case "verify":
		return modeVerify
	case "artifact":
		return modeArtifact
	default:
		return modeRepair
	}
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
