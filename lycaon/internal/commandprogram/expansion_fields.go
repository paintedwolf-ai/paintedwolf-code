package commandprogram

import (
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// fieldAssembly accumulates expanded pieces into fields. A field exists once
// any piece reaches it, including quoted empty text; unquoted expansion output
// splits on space, tab and LF, never on a configurable IFS.
type fieldAssembly struct {
	limits   *expansionLimits
	span     toolcontract.SourceSpan
	charge   func(int64) error
	current  []expansionPiece
	present  bool
	complete [][]expansionPiece
}

func (a *fieldAssembly) add(text string, quoted bool) error {
	if strings.IndexByte(text, 0) >= 0 {
		return invalidValue(a.span, "expanded values without NUL bytes")
	}
	if err := a.charge(pieceBytes); err != nil {
		return err
	}
	a.current = append(a.current, expansionPiece{text: text, quoted: quoted})
	a.present = true
	return nil
}

func (a *fieldAssembly) split(text string) error {
	start := 0
	for i := 0; i <= len(text); i++ {
		if i < len(text) && text[i] != ' ' && text[i] != '\t' && text[i] != '\n' {
			continue
		}
		if i > start {
			if err := a.add(text[start:i], false); err != nil {
				return err
			}
		}
		if i < len(text) && a.present {
			if err := a.push(); err != nil {
				return err
			}
		}
		start = i + 1
	}
	return nil
}

// push completes the current field under expanded_fields.
func (a *fieldAssembly) push() error {
	if count := int64(len(a.complete) + 1); count > a.limits.fields {
		return a.limits.exceeded("expanded_fields", count, a.span)
	}
	a.complete = append(a.complete, a.current)
	a.current, a.present = nil, false
	return nil
}

func (a *fieldAssembly) finish() error {
	if !a.present {
		return nil
	}
	return a.push()
}
