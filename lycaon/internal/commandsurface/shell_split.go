package commandsurface

import "strings"

// SplitCommandLines divides command text into the lines a shell reads as
// separate commands. Quotes are honored so a newline inside an argument stays
// part of it, and a trailing backslash continues the command onto the next line.
func SplitCommandLines(raw string) []string {
	var (
		out                []string
		cur                strings.Builder
		inSingle, inDouble bool
	)
	flush := func() {
		if line := strings.Join(strings.Fields(cur.String()), " "); line != "" {
			out = append(out, line)
		}
		cur.Reset()
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			cur.WriteByte(c)
		case c == '"' && !inSingle:
			inDouble = !inDouble
			cur.WriteByte(c)
		case (c == '\n' || c == '\r') && !inSingle && !inDouble:
			if text := cur.String(); trailingBackslashes(text)%2 == 1 {
				cur.Reset()
				cur.WriteString(text[:len(text)-1] + " ")
				continue
			}
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

func trailingBackslashes(s string) int {
	n := 0
	for i := len(s) - 1; i >= 0 && s[i] == '\\'; i-- {
		n++
	}
	return n
}

// SplitExecutionGroups divides a command into its independent pipelines. Each
// still holds its own `|` stages, which SplitPipelineStages separates.
func SplitExecutionGroups(normalized string) [][]string {
	if normalized == "" {
		return nil
	}
	tokens := TokenizeShell(normalized)
	var groups [][]string
	var current []string
	flush := func() {
		if len(current) > 0 {
			groups = append(groups, current)
			current = nil
		}
	}
	for _, tok := range tokens {
		switch tok {
		case ";", "&&", "||", "&":
			flush()
		default:
			current = append(current, tok)
		}
	}
	flush()
	return groups
}

// SplitPipelineStages keeps image and command fields on one process event.
func SplitPipelineStages(tokens []string) [][]string {
	var stages [][]string
	var current []string
	flush := func() {
		if len(current) > 0 {
			stages = append(stages, current)
			current = nil
		}
	}
	for _, tok := range tokens {
		if tok == "|" {
			flush()
			continue
		}
		current = append(current, tok)
	}
	flush()
	return stages
}

// IsStageSeparator reports pipeline and list separators.
func IsStageSeparator(tok string) bool {
	switch tok {
	case "|", ";", "&&", "||":
		return true
	default:
		return false
	}
}

// IsShellOperator reports unquoted shell operators TokenizeShell isolates.
func IsShellOperator(tok string) bool {
	switch tok {
	case "|", "||", "&&", ";", "&":
		return true
	default:
		return false
	}
}

// TokenizeShell splits on whitespace and isolates unquoted shell operators.
// Quoted regions keep their surrounding quotes.
func TokenizeShell(s string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inSingle {
			b.WriteByte(c)
			if c == '\'' {
				inSingle = false
			}
			continue
		}
		if inDouble {
			b.WriteByte(c)
			if c == '"' {
				inDouble = false
			}
			continue
		}
		switch c {
		case '\'':
			inSingle = true
			b.WriteByte(c)
		case '"':
			inDouble = true
			b.WriteByte(c)
		case ' ', '\t', '\n', '\r':
			flush()
		case '|':
			flush()
			if i+1 < len(s) && s[i+1] == '|' {
				out = append(out, "||")
				i++
			} else {
				out = append(out, "|")
			}
		case '&':
			flush()
			if i+1 < len(s) && s[i+1] == '&' {
				out = append(out, "&&")
				i++
			} else {
				out = append(out, "&")
			}
		case ';':
			flush()
			out = append(out, ";")
		default:
			b.WriteByte(c)
		}
	}
	flush()
	return out
}
