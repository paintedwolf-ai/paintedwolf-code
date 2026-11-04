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

// nodeScanner recognizes one custom node from its '[', one byte at a time.
type nodeScanner struct {
	opts Options
	buf  []byte
	mode bodyMode
	// confirmed means this cannot be ordinary prose any longer. A lone bare
	// member stays unconfirmed until it ends, so that prose releases at once.
	confirmed bool
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
	s.confirmed, s.afterLexeme = false, false
	s.inQuote, s.escaped, s.inAngle = false, false, false
	s.depth = 0
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

// feedLexeme handles the first member of an unconfirmed node, where each byte
// may disqualify it.
func (s *nodeScanner) feedLexeme(b byte) scanResult {
	if s.afterLexeme {
		return s.feedAfterLexeme(b)
	}
	switch b {
	case '"':
		if len(s.buf) != 1 {
			return scanRelease
		}
		return s.confirm(modeCitation, b)
	case '#':
		if !handlePrefix.Match(s.buf[1:]) {
			return scanRelease
		}
		return s.confirm(modeCitation, b)
	case ':':
		if isReservedName(string(s.buf[1:])) {
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
		return s.confirm(modeCitation, b)
	case ' ', '\t':
		if !s.lexemeAcceptable() {
			return scanRelease
		}
		s.buf = append(s.buf, b)
		s.afterLexeme = true
		return scanPending
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
	case '"', ',':
		return s.confirm(modeCitation, b)
	case ']':
		s.buf = append(s.buf, b)
		return scanClosed
	default:
		return scanRelease
	}
}

// lexemeAcceptable decides a bare first member at its terminator.
func (s *nodeScanner) lexemeAcceptable() bool {
	lexeme := string(s.buf[1:])
	if lexeme == "" || isStructuralBracket(lexeme) {
		return false
	}
	path, start, _, ok := splitCoordinate(lexeme)
	return ok && validBarePath(path) && isCitationAttempt(path, start, s.opts)
}

// isCitationAttempt decides a bare path by structure. A coordinate makes it an
// attempt unless the path part is all digits, as in a time or a ratio. Without
// one, only an observed path is a citation, whatever its segment count.
func isCitationAttempt(path, start string, opts Options) bool {
	if start != "" {
		return !isAllDigits(path)
	}
	return opts.known(path)
}

func (s *nodeScanner) confirm(mode bodyMode, b byte) scanResult {
	s.mode = mode
	s.confirmed = true
	s.inQuote = mode == modeCitation && b == '"'
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
