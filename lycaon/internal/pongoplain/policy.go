package pongoplain

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrSourceLimit      = errors.New("pongoplain: template source limit exceeded")
	ErrOutputLimit      = errors.New("pongoplain: rendered output limit exceeded")
	ErrContextLimit     = errors.New("pongoplain: render context limit exceeded")
	ErrExecutionLimit   = errors.New("pongoplain: execution limit exceeded")
	ErrNondeterministic = errors.New("pongoplain: nondeterministic template operation")
	ErrComposition      = errors.New("pongoplain: invalid template composition")
)

// Analysis is the structurally verified part of a template.
type Analysis struct{ Dependencies []string }

// Inspect applies limits before parsing or loading source.
func Inspect(source string, profile Profile) (Analysis, error) {
	if len(source) > MaxSourceBytes {
		return Analysis{}, fmt.Errorf("%w: %d > %d bytes", ErrSourceLimit, len(source), MaxSourceBytes)
	}
	if _, err := bansFor(profile); err != nil {
		return Analysis{}, err
	}

	var analysis Analysis
	forDepth := 0
	tagCount := 0
	opaqueEnd := ""
	for offset := 0; ; {
		start := strings.Index(source[offset:], "{%")
		if start < 0 {
			break
		}
		start += offset
		end := strings.Index(source[start+2:], "%}")
		if end < 0 {
			break
		}
		end += start + 2
		body := strings.TrimSpace(source[start+2 : end])
		body = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(body, "-"), "-"))
		fields := strings.Fields(body)
		if len(fields) == 0 {
			offset = end + 2
			continue
		}
		tagCount++
		if tagCount > MaxTemplateTags {
			return Analysis{}, fmt.Errorf("%w: more than %d tags", ErrSourceLimit, MaxTemplateTags)
		}
		if opaqueEnd != "" {
			if fields[0] == opaqueEnd {
				opaqueEnd = ""
			}
			offset = end + 2
			continue
		}
		switch fields[0] {
		case "comment":
			opaqueEnd = "endcomment"
		case "verbatim":
			opaqueEnd = "endverbatim"
		case "for":
			forDepth++
			if forDepth > MaxForNesting {
				return Analysis{}, fmt.Errorf("%w: for nesting exceeds %d", ErrExecutionLimit, MaxForNesting)
			}
		case "endfor":
			if forDepth > 0 {
				forDepth--
			}
		case "now":
			return Analysis{}, fmt.Errorf("%w: tag now", ErrNondeterministic)
		case "include":
			if profile != Composed {
				break
			}
			dep, ok := quotedDependency(strings.TrimSpace(strings.TrimPrefix(body, fields[0])))
			if !ok {
				return Analysis{}, fmt.Errorf("%w: %s dependency must be a string literal", ErrComposition, fields[0])
			}
			analysis.Dependencies = append(analysis.Dependencies, dep)
		}
		offset = end + 2
	}
	return analysis, nil
}

func quotedDependency(rest string) (string, bool) {
	if len(rest) < 2 || (rest[0] != '\'' && rest[0] != '"') {
		return "", false
	}
	quote := rest[0]
	var b strings.Builder
	escaped := false
	for i := 1; i < len(rest); i++ {
		c := rest[i]
		if escaped {
			b.WriteByte(c)
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == quote {
			if strings.TrimSpace(rest[i+1:]) != "" {
				return "", false
			}
			return b.String(), b.Len() > 0
		}
		b.WriteByte(c)
	}
	return "", false
}
