package commandprogram

import (
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// fieldAssembly accumulates expanded pieces into fields. A field exists once
// any piece reaches it, including quoted empty text; unquoted expansion output
// splits on the frame's IFS when it is set.
type fieldAssembly struct {
	limits   *expansionLimits
	span     toolcontract.SourceSpan
	charge   func(int64) error
	ifs      string
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

func (a *fieldAssembly) addSubstitution(subst *ProcessSubstitution) error {
	text := subst.Path()
	if strings.IndexByte(text, 0) >= 0 {
		return invalidValue(a.span, "expanded values without NUL bytes")
	}
	if err := a.charge(pieceBytes); err != nil {
		return err
	}
	a.current = append(a.current, expansionPiece{text: text, quoted: true, subst: subst})
	a.present = true
	return nil
}

func (a *fieldAssembly) split(text string) error {
	start := 0
	for i := 0; i <= len(text); i++ {
		if i < len(text) && strings.IndexByte(a.ifs, text[i]) < 0 {
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
