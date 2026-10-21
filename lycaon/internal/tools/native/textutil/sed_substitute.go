package textutil

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
)

// PosixRegex is one compiled BRE or ERE with POSIX leftmost-longest matching
// and POSIX subexpression offsets.
type PosixRegex interface {
	// Find returns the leftmost-longest match starting at or after from as
	// [start, end, group1 start, group1 end, ...] (-1 when a group is unset),
	// or nil. Anchors and assertions see the whole subject.
	Find(ctx context.Context, subject []byte, from int) ([]int, error)
	// Groups is the number of capture groups a replacement may name.
	Groups() int
}

// PosixRegexEngine compiles sed's regular expressions. It must be the shared
// POSIX BRE/ERE engine; sed never runs on RE2.
type PosixRegexEngine interface {
	Compile(ctx context.Context, pattern string, extended bool) (PosixRegex, error)
}

// sedRegexError is the shared engine's refusal of one script regex.
type sedRegexError struct {
	offset int
	cause  error
}

func (e *sedRegexError) Error() string { return e.cause.Error() }
func (e *sedRegexError) Unwrap() error { return e.cause }

func (p *sedParser) substitution() (*sedSubstitution, error) {
	delimiter := p.peek()
	if delimiter == 0 || delimiter == '\n' || delimiter == '\\' {
		return nil, p.fail("an s delimiter other than newline or backslash")
	}
	p.pos++
	pattern, err := p.delimited(delimiter)
	if err != nil {
		return nil, err
	}
	regex, err := p.compile(pattern)
	if err != nil {
		return nil, err
	}
	sub := &sedSubstitution{regex: regex}
	if sub.replacement, err = p.replacement(delimiter, regex.Groups()); err != nil {
		return nil, err
	}
	for p.pos < len(p.src) {
		switch c := p.src[p.pos]; {
		case c == 'g' && !sub.global:
			sub.global = true
		case c == 'p' && !sub.print:
			sub.print = true
		case c >= '1' && c <= '9' && sub.ordinal == 0:
			start := p.pos
			for p.pos+1 < len(p.src) && p.src[p.pos+1] >= '0' && p.src[p.pos+1] <= '9' {
				p.pos++
			}
			sub.ordinal, _ = strconv.Atoi(p.src[start : p.pos+1])
		case c == 'w' || c == 'W' || c == 'e' || c == 'I' || c == 'i' || c == 'M' || c == 'm':
			return nil, p.unsupported("s flags g, p and a match ordinal")
		default:
			return sub, nil
		}
		p.pos++
	}
	return sub, nil
}

// replacement reads & (the match), \1..\9, \n (newline), escaped bytes and
// literal text up to the delimiter. A group beyond the regex refuses.
func (p *sedParser) replacement(delimiter byte, groups int) ([]sedPiece, error) {
	var pieces []sedPiece
	var literal strings.Builder
	flush := func() {
		if literal.Len() > 0 {
			pieces = append(pieces, sedPiece{literal: literal.String(), group: -1})
			literal.Reset()
		}
	}
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		p.pos++
		switch {
		case c == delimiter:
			flush()
			return pieces, nil
		case c == '&':
			flush()
			pieces = append(pieces, sedPiece{group: 0})
		case c == '\\' && p.pos < len(p.src):
			e := p.src[p.pos]
			p.pos++
			switch {
			case e >= '1' && e <= '9':
				if int(e-'0') > groups {
					return nil, p.fail("a capture group the regex defines")
				}
				flush()
				pieces = append(pieces, sedPiece{group: int(e - '0')})
			case e == 'n':
				literal.WriteByte('\n')
			default:
				literal.WriteByte(e)
			}
		default:
			literal.WriteByte(c)
		}
	}
	return nil, p.fail("a terminating " + string(delimiter))
}

// substitute replaces the selected matches: the ordinal-th only, or with g
// every match from the ordinal-th (default first). An empty match directly
// after a previous match is skipped, as POSIX requires, and the search then
// advances one scalar so it always terminates.
func (r *sedRun) substitute(s *sedSubstitution, space []byte) ([]byte, bool, error) {
	ordinal := max(s.ordinal, 1)
	var out []byte
	copied, from, count, previousEnd := 0, 0, 0, -1
	replaced := false
	for from <= len(space) {
		match, err := s.regex.Find(r.inv.ctx, space, from)
		if err != nil {
			return nil, false, err
		}
		if match == nil {
			break
		}
		start, end := match[0], match[1]
		if start == end && start == previousEnd {
			from = start + scalarWidth(space, start)
			continue
		}
		count++
		if count >= ordinal && (s.global || count == ordinal) {
			out = append(out, space[copied:start]...)
			out = appendReplacement(out, s.replacement, space, match)
			copied, replaced = end, true
			if limit := r.inv.limit("record_bytes"); int64(len(out)) > limit {
				return nil, false, nativeutility.LimitExceeded("record_bytes", limit, int64(len(out)))
			}
			if !s.global {
				break
			}
		}
		previousEnd = end
		if end > start {
			from = end
		} else {
			from = end + scalarWidth(space, end)
		}
	}
	if !replaced {
		return space, false, nil
	}
	out = append(out, space[copied:]...)
	if err := r.inv.reserve(int64(len(out))); err != nil {
		return nil, false, err
	}
	return out, s.print, nil
}

func appendReplacement(out []byte, pieces []sedPiece, space []byte, match []int) []byte {
	for _, piece := range pieces {
		if piece.group < 0 {
			out = append(out, piece.literal...)
			continue
		}
		if start, end := match[2*piece.group], match[2*piece.group+1]; start >= 0 && end >= start {
			out = append(out, space[start:end]...)
		}
	}
	return out
}

func scalarWidth(b []byte, at int) int {
	if at >= len(b) {
		return 1
	}
	_, size := utf8.DecodeRune(b[at:])
	return size
}
