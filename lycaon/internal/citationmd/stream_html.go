package citationmd

import (
	"regexp"
	"strings"
)

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
	s.mode, s.raw.closer, s.raw.holding = modeRawLiteral, closer, false
	s.raw.tail = s.raw.tail[:0]
	p.machine.skipTo(p.at + 1)
}

// rawLiteralByte is codeByte's counterpart for a string closer.
func (p *Projector) rawLiteralByte(b byte) bool {
	s := &p.inline
	s.raw.tail = append(s.raw.tail, b)
	if len(s.raw.tail) > len(s.raw.closer) {
		s.raw.tail = s.raw.tail[1:]
	}
	if !s.raw.holding && b == '[' {
		s.raw.holding = true
		s.held = s.held[:0]
		s.holdStart = p.at
	}
	if s.raw.holding {
		s.held = append(s.held, b)
	} else {
		p.emitRaw(b)
	}
	if string(s.raw.tail) == s.raw.closer {
		if s.raw.holding {
			p.out.text(s.held)
		}
		s.mode, s.raw.holding = modeText, false
		p.machine.skipTo(p.at + 1)
		return true
	}
	if s.raw.holding && len(s.held) >= MaxInlineProtocolNodeBytes {
		p.failHold(holdAngle)
	}
	return true
}

func isASCIILetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
