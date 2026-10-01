package editorconfig

import (
	"regexp"
	"strconv"
	"strings"
)

// MatchPattern reports whether a section glob selects a path relative to the
// config directory. Slash-free patterns match basenames; a leading slash or
// any other slash anchors the pattern to the config directory.
func MatchPattern(pattern, rel string) bool {
	path := strings.ReplaceAll(rel, "\\", "/")
	name := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		name = path[i+1:]
	}
	anchored := strings.HasPrefix(pattern, "/")
	glob := strings.TrimPrefix(pattern, "/")
	re, err := regexp.Compile("^" + globExpression(glob) + "$")
	if err != nil {
		return false
	}
	if anchored || strings.Contains(glob, "/") {
		return re.MatchString(path)
	}
	return re.MatchString(name)
}

// The widest numeric brace range expanded into alternatives.
const maxBraceRange = 10_000

// globExpression translates EditorConfig glob syntax to an unanchored RE2 body.
func globExpression(pattern string) string {
	var out strings.Builder
	for i := 0; i < len(pattern); {
		switch ch := pattern[i]; ch {
		case '\\':
			if i+1 >= len(pattern) {
				out.WriteString(`\\`)
				i++
				continue
			}
			out.WriteString(regexp.QuoteMeta(pattern[i+1 : i+2]))
			i += 2
		case '*':
			switch {
			case strings.HasPrefix(pattern[i:], "**/"):
				out.WriteString("(?:.*/)?")
				i += 3
			case strings.HasPrefix(pattern[i:], "**"):
				out.WriteString(".*")
				i += 2
			default:
				out.WriteString("[^/]*")
				i++
			}
		case '?':
			out.WriteString("[^/]")
			i++
		case '[':
			end := strings.IndexByte(pattern[i+1:], ']')
			if end < 0 {
				out.WriteString(`\[`)
				i++
				continue
			}
			out.WriteString(characterClass(pattern[i+1 : i+1+end]))
			i += end + 2
		case '{':
			end := braceEnd(pattern, i)
			if end < 0 {
				out.WriteString(`\{`)
				i++
				continue
			}
			inner := pattern[i+1 : end]
			if alts, ok := braceAlternatives(inner); ok {
				parts := make([]string, len(alts))
				for j, alt := range alts {
					parts[j] = globExpression(alt)
				}
				out.WriteString("(?:" + strings.Join(parts, "|") + ")")
			} else {
				// `{s1}` with no comma or range matches literally.
				out.WriteString(regexp.QuoteMeta("{" + inner + "}"))
			}
			i = end + 1
		default:
			out.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
			i++
		}
	}
	return out.String()
}

// characterClass treats every member literally.
func characterClass(body string) string {
	negated := strings.HasPrefix(body, "!") || strings.HasPrefix(body, "^")
	if negated {
		body = body[1:]
	}
	var out strings.Builder
	out.WriteByte('[')
	if negated {
		out.WriteByte('^')
	}
	for _, r := range body {
		switch r {
		case '\\', '^', ']', '-', '[':
			out.WriteByte('\\')
		}
		out.WriteRune(r)
	}
	out.WriteByte(']')
	return out.String()
}

func braceEnd(pattern string, start int) int {
	depth := 0
	for i := start; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\':
			i++
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

var numericRange = regexp.MustCompile(`^(-?\d+)\.\.(-?\d+)$`)

// braceAlternatives expands `{a,b}` and `{n..m}`; false means literal braces.
func braceAlternatives(inner string) ([]string, bool) {
	if !strings.Contains(inner, ",") && !numericRange.MatchString(inner) {
		return nil, false
	}
	var out []string
	for _, alt := range splitBrace(inner) {
		if m := numericRange.FindStringSubmatch(alt); m != nil {
			lo, errLo := strconv.Atoi(m[1])
			hi, errHi := strconv.Atoi(m[2])
			if errLo == nil && errHi == nil && lo < hi && hi-lo <= maxBraceRange {
				for n := lo; n <= hi; n++ {
					out = append(out, strconv.Itoa(n))
				}
				continue
			}
		}
		out = append(out, alt)
	}
	return out, true
}

func splitBrace(inner string) []string {
	var alts []string
	depth, start := 0, 0
	for i := 0; i < len(inner); i++ {
		switch inner[i] {
		case '\\':
			i++
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				alts = append(alts, inner[start:i])
				start = i + 1
			}
		}
	}
	return append(alts, inner[start:])
}
