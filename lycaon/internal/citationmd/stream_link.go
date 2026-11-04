package citationmd

import (
	"bytes"
	"regexp"
	"strings"
	"unicode"
)

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
		s.brackets.afterClose = false
		return false
	}
	s.held = append(s.held[:0], b)
	s.holdStart = p.at
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
	case destinationPending:
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
	p.settleDefinition()
	return false
}

// settleDefinition decides a held definition at its end, with the next byte
// unconsumed.
func (p *Projector) settleDefinition() {
	s := &p.inline
	if !definitionTail.Match(s.held) {
		p.failNodeHold(holdDestination)
		return
	}
	label := s.brackets.label
	if s.pending != nil {
		label = s.pending.token[1 : len(s.pending.token)-1]
	}
	p.defs[normalizeLabel(string(label))] = true
	p.blocks.paragraph = false
	saved := p.at
	p.at--
	p.releaseHold()
	p.at = saved
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
		s.brackets.labelDepth++
	case ']':
		s.brackets.labelDepth--
		if s.brackets.labelDepth == 0 {
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
	switch b {
	case ' ', '\t', '\n', '<':
		*u = urlTracker{word: u.word[:0]}
		return false
	case '*', '_', '~', '(':
		if u.stage != urlPath {
			*u = urlTracker{word: u.word[:0]}
			return false
		}
	}
	if u.stage == urlPath {
		return true
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
