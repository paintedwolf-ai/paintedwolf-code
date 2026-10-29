package commandprogram

import (
	"context"
	"regexp/syntax"
	"strconv"
	"strings"
	"unicode/utf8"
)

// repeatTag binds an admitted source interval to an otherwise ordinary capture.
// Captures keep distinct counted atoms distinct during the POSIX parser's
// alternation factoring. Predicate matching exposes no capture results.
type repeatTag struct{ at, minimum, maximum int }

func normalizePredicateRepeats(ctx context.Context, source string) (string, map[int]repeatTag, error) {
	var out strings.Builder
	tags := []repeatTag{}
	stack := []int{}
	atom := -1
	for i := 0; i < len(source); {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		start := i
		switch source[i] {
		case '\\':
			i++
			if i < len(source) {
				_, width := utf8.DecodeRuneInString(source[i:])
				i += width
			}
			atom = out.Len()
			out.WriteString(source[start:i])
		case '[':
			i++
			if i < len(source) && source[i] == '^' {
				i++
			}
			if i < len(source) && source[i] == ']' {
				i++
			}
			for i < len(source) {
				if source[i] == '\\' && i+1 < len(source) {
					i += 2
					continue
				}
				if source[i] == '[' && i+1 < len(source) && strings.ContainsRune(":.=", rune(source[i+1])) {
					end := "" + string(source[i+1]) + "]"
					if j := strings.Index(source[i+2:], end); j >= 0 {
						i += j + 4
						continue
					}
				}
				if source[i] == ']' {
					i++
					break
				}
				i++
			}
			atom = out.Len()
			out.WriteString(source[start:i])
		case '(':
			stack = append(stack, out.Len())
			out.WriteByte('(')
			i++
			atom = -1
		case ')':
			out.WriteByte(')')
			i++
			atom = -1
			if len(stack) > 0 {
				atom = stack[len(stack)-1]
				stack = stack[:len(stack)-1]
			}
		case '|':
			out.WriteByte('|')
			i++
			atom = -1
		case '*', '+', '?':
			out.WriteByte(source[i])
			i++
		case '{':
			end := strings.IndexByte(source[i:], '}')
			if end < 0 {
				out.WriteByte(source[i])
				i++
				atom = out.Len() - 1
				continue
			}
			end += i
			minimum, maximum, ok := predicateRepeatRange(source[i+1 : end])
			if !ok {
				out.WriteString(source[i : end+1])
				i = end + 1
				atom = out.Len() - 1
				continue
			}
			if atom < 0 {
				return "", nil, errPredicateUsage
			}
			// A source-sized bound is sufficient to hold the lexical skeleton: every
			// replacement adds only two delimiters and a fixed {1} token.
			prefix := out.String()
			out.Reset()
			out.WriteString(prefix[:atom])
			out.WriteByte('(')
			out.WriteString(prefix[atom:])
			out.WriteString("){1}")
			for j := range tags {
				if tags[j].at >= atom {
					tags[j].at++
				}
			}
			for j := range stack {
				if stack[j] >= atom {
					stack[j]++
				}
			}
			tags = append(tags, repeatTag{atom, minimum, maximum})
			i = end + 1
		default:
			atom = out.Len()
			_, width := utf8.DecodeRuneInString(source[i:])
			out.WriteString(source[i : i+width])
			i += width
		}
	}
	skeleton := out.String()
	byOffset := map[int]repeatTag{}
	for _, tag := range tags {
		byOffset[tag.at] = tag
	}
	captures := map[int]repeatTag{}
	number := 0
	// The same class/escape lexer already admitted the original. Counting capture
	// openings here ignores quoted delimiters and POSIX bracket subexpressions.
	for i := 0; i < len(skeleton); i++ {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		if skeleton[i] == '\\' {
			i++
			continue
		}
		if skeleton[i] == '[' {
			i++
			if i < len(skeleton) && skeleton[i] == '^' {
				i++
			}
			if i < len(skeleton) && skeleton[i] == ']' {
				i++
			}
			for i < len(skeleton) {
				if skeleton[i] == '\\' && i+1 < len(skeleton) {
					i += 2
					continue
				}
				if skeleton[i] == '[' && i+1 < len(skeleton) && strings.ContainsRune(":.=", rune(skeleton[i+1])) {
					ending := string(skeleton[i+1]) + "]"
					if j := strings.Index(skeleton[i+2:], ending); j >= 0 {
						i += j + 4
						continue
					}
				}
				if skeleton[i] == ']' {
					break
				}
				i++
			}
			continue
		}
		if skeleton[i] == '(' {
			number++
			if tag, ok := byOffset[i]; ok {
				captures[number] = tag
			}
		}
	}
	return skeleton, captures, nil
}
func predicateRepeatRange(text string) (int, int, bool) {
	fields := strings.Split(text, ",")
	if len(fields) > 2 || fields[0] == "" {
		return 0, 0, false
	}
	parse := func(s string) (int, bool) {
		if s == "" {
			return 0, false
		}
		for i := range len(s) {
			if s[i] < '0' || s[i] > '9' {
				return 0, false
			}
		}
		n, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return int(^uint32(0)), true
		}
		return int(n), true
	}
	if len(fields) == 2 && fields[1] != "" {
		a, aok := decimalOperand(fields[0])
		b, bok := decimalOperand(fields[1])
		if aok && bok && compareDecimal(a, b) > 0 {
			return 0, 0, false
		}
	}
	minimum, ok := parse(fields[0])
	if !ok {
		return 0, 0, false
	}
	maximum := minimum
	if len(fields) == 2 {
		if fields[1] == "" {
			maximum = -1
		} else {
			maximum, ok = parse(fields[1])
			if !ok {
				return 0, 0, false
			}
		}
	}
	if maximum >= 0 && maximum < minimum {
		return 0, 0, false
	}
	return minimum, maximum, true
}

// restorePredicateRepeats restores counted intervals in the validated tree.
// Compilation stays counted in the selected VM; it never allocates m copies.
func restorePredicateRepeats(ctx context.Context, re *syntax.Regexp, tags map[int]repeatTag) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, sub := range re.Sub {
		if err := restorePredicateRepeats(ctx, sub, tags); err != nil {
			return err
		}
	}
	if re.Op != syntax.OpRepeat {
		return nil
	}
	if len(re.Sub) != 1 || re.Sub[0].Op != syntax.OpCapture {
		return errPredicateUsage
	}
	tag, ok := tags[re.Sub[0].Cap]
	if !ok {
		return errPredicateUsage
	}
	re.Sub[0] = re.Sub[0].Sub[0]
	re.Min = tag.minimum
	re.Max = tag.maximum
	return nil
}
