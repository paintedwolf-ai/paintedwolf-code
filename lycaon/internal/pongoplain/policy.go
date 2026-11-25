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
type Analysis struct {
	Dependencies []string
	Branches     []BranchSpec
}

// Inspect applies limits and discovers static dependencies and branches before parsing.
func Inspect(source string, profile Profile) (Analysis, error) {
	if len(source) > MaxSourceBytes {
		return Analysis{}, fmt.Errorf("%w: %d > %d bytes", ErrSourceLimit, len(source), MaxSourceBytes)
	}
	if _, err := bansFor(profile); err != nil {
		return Analysis{}, err
	}

	var analysis Analysis
	lines := newLineCursor(source)
	forDepth := 0
	tagCount := 0
	opaqueEnd := ""
	for offset := 0; offset < len(source); {
		nextTag, nextComment := nextTemplateDelimiter(source, offset)
		if nextTag < 0 {
			break
		}
		if opaqueEnd == "" && nextComment >= 0 && nextComment < nextTag {
			closeComment := strings.Index(source[nextComment+2:], "#}")
			if closeComment < 0 {
				break
			}
			offset = nextComment + 2 + closeComment + 2
			continue
		}
		start := nextTag
		end := strings.Index(source[start+2:], "%}")
		if end < 0 {
			break
		}
		end += start + 2
		rawInner := source[start+2 : end]
		kwOffset, tag, rest := parseTagHeader(rawInner, start+2)
		if tag == "" {
			offset = end + 2
			continue
		}
		tagCount++
		if tagCount > MaxTemplateTags {
			return Analysis{}, fmt.Errorf("%w: more than %d tags", ErrSourceLimit, MaxTemplateTags)
		}
		if opaqueEnd != "" {
			if tag == opaqueEnd {
				opaqueEnd = ""
			}
			offset = end + 2
			continue
		}
		if err := applyInspectedTag(&analysis, lines, profile, tag, rest, kwOffset, &forDepth, &opaqueEnd); err != nil {
			return Analysis{}, err
		}
		offset = end + 2
	}
	return analysis, nil
}

func nextTemplateDelimiter(source string, offset int) (tagIdx, commentIdx int) {
	tagIdx = strings.Index(source[offset:], "{%")
	if tagIdx >= 0 {
		tagIdx += offset
	}
	commentIdx = strings.Index(source[offset:], "{#")
	if commentIdx >= 0 {
		commentIdx += offset
	}
	return tagIdx, commentIdx
}

func parseTagHeader(rawInner string, baseOffset int) (kwOffset int, tag, rest string) {
	trimmedRight := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rawInner), "-"))
	idx := 0
	if idx < len(rawInner) && rawInner[idx] == '-' {
		idx++
	}
	for idx < len(rawInner) && (rawInner[idx] == ' ' || rawInner[idx] == '\t' || rawInner[idx] == '\n' || rawInner[idx] == '\r') {
		idx++
	}
	fields := strings.Fields(trimmedRight)
	if len(fields) == 0 {
		return 0, "", ""
	}
	tag = strings.TrimPrefix(fields[0], "-")
	if tag == "" {
		if len(fields) < 2 {
			return 0, "", ""
		}
		tag = fields[1]
	}
	// tag is a field of rawInner after its trim marker and leading space.
	kwStart := idx + strings.Index(rawInner[idx:], tag)
	after := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rawInner[kwStart+len(tag):]), "-"))
	return baseOffset + kwStart, tag, after
}

func applyInspectedTag(
	analysis *Analysis,
	lines *lineCursor,
	profile Profile,
	tag, rest string,
	kwOffset int,
	forDepth *int,
	opaqueEnd *string,
) error {
	appendBranch := func(kind BranchKind, expr string) {
		line, col := lines.at(kwOffset)
		analysis.Branches = append(analysis.Branches, BranchSpec{
			Index:      len(analysis.Branches),
			Line:       line,
			Col:        col,
			Kind:       kind,
			Expression: expr,
		})
	}
	switch tag {
	case "comment":
		*opaqueEnd = "endcomment"
	case "verbatim":
		*opaqueEnd = "endverbatim"
	case "if":
		appendBranch(BranchIf, rest)
	case "elif":
		appendBranch(BranchElif, rest)
	case "else":
		appendBranch(BranchElse, "")
	case "for":
		*forDepth++
		if *forDepth > MaxForNesting {
			return fmt.Errorf("%w: for nesting exceeds %d", ErrExecutionLimit, MaxForNesting)
		}
		appendBranch(BranchFor, rest)
	case "empty":
		appendBranch(BranchEmpty, "")
	case "endfor":
		if *forDepth > 0 {
			*forDepth--
		}
	case "now":
		return fmt.Errorf("%w: tag now", ErrNondeterministic)
	case "include":
		if profile != Composed {
			return nil
		}
		dep, _, err := parseIncludeClause(rest)
		if err != nil || dep == "" {
			return fmt.Errorf("%w: %s dependency must be a string literal", ErrComposition, tag)
		}
		analysis.Dependencies = append(analysis.Dependencies, dep)
	}
	return nil
}

// lineCursor maps nondecreasing byte offsets to 1-based line and column,
// scanning each byte of the source at most once.
type lineCursor struct {
	source    string
	offset    int
	line      int
	lineStart int
}

func newLineCursor(source string) *lineCursor { return &lineCursor{source: source, line: 1} }

func (c *lineCursor) at(offset int) (line, col int) {
	offset = min(max(offset, 0), len(c.source))
	if offset < c.offset {
		c.offset, c.line, c.lineStart = 0, 1, 0
	}
	segment := c.source[c.offset:offset]
	if newlines := strings.Count(segment, "\n"); newlines > 0 {
		c.line += newlines
		c.lineStart = c.offset + strings.LastIndexByte(segment, '\n') + 1
	}
	c.offset = offset
	return c.line, 1 + offset - c.lineStart
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
