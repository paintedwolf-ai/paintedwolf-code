package toolcommand

import (
	"strings"
	"unicode"
)

type bashWord struct {
	raw     string
	value   string
	literal bool
}

type bashRedirect struct {
	op     string
	target bashWord
}

type bashHeredoc struct {
	stripTabs  bool
	quoted     bool
	delim      string
	body       string
	terminated bool
	literal    bool
}

type bashHerestring struct {
	word    bashWord
	body    string
	literal bool
}

type bashStage struct {
	env         []string
	argv        []bashWord
	redirects   []bashRedirect
	heredocs    []bashHeredoc
	herestrings []bashHerestring
	subshell    string
}

type bashStatement struct {
	op     string
	stages []bashStage
}

type rawToken struct {
	kind      string // "word", "op", "group"
	raw       string
	heredoc   *bashHeredoc
	herestr   *bashHerestring
	groupBody string
}

// lexBashScript tokenizes a shell script while attaching here-document bodies
// to the << or <<- operators that declared them on each physical line.
func lexBashScript(src string) []rawToken {
	var tokens []rawToken
	var pending []*bashHeredoc
	i, n := 0, len(src)

	consumeHeredocs := func() {
		for _, hd := range pending {
			var body strings.Builder
			for i < n {
				lineEnd := strings.IndexByte(src[i:], '\n')
				var line string
				hasNL := false
				if lineEnd < 0 {
					line = src[i:]
					i = n
				} else {
					line = src[i : i+lineEnd]
					i += lineEnd + 1
					hasNL = true
				}
				trimmedCR := strings.TrimSuffix(line, "\r")
				checkLine := trimmedCR
				if hd.stripTabs {
					checkLine = strings.TrimLeft(checkLine, "\t")
				}
				if checkLine == hd.delim {
					hd.terminated = true
					break
				}
				if hd.stripTabs {
					line = strings.TrimLeft(line, "\t")
				}
				body.WriteString(line)
				if hasNL {
					body.WriteByte('\n')
				}
			}
			hd.body = body.String()
			hd.literal = hd.terminated && (hd.quoted || !hasUnquotedShellExpansion(hd.body))
		}
		pending = nil
	}

	for i < n {
		c := src[i]
		if c == ' ' || c == '\t' || c == '\r' {
			i++
			continue
		}
		if c == '#' {
			for i < n && src[i] != '\n' {
				i++
			}
			continue
		}
		if c == '\n' {
			i++
			consumeHeredocs()
			tokens = append(tokens, rawToken{kind: "op", raw: "\n"})
			continue
		}
		if c == '(' || (c == '{' && (i+1 == n || isShellSpace(src[i+1]))) {
			groupRaw, inner, next, ok := scanBalancedGroup(src, i)
			if ok {
				tokens = append(tokens, rawToken{kind: "group", raw: groupRaw, groupBody: inner})
				i = next
				continue
			}
		}
		if op, next, ok := scanOperatorToken(src, i); ok {
			i = next
			if op == "<<" || op == "<<-" {
				for i < n && (src[i] == ' ' || src[i] == '\t') {
					i++
				}
				delimRaw, afterDelim := scanShellWord(src, i)
				i = afterDelim
				dw := parseBashWord(delimRaw)
				quoted := strings.ContainsAny(delimRaw, "'\"\\")
				hd := &bashHeredoc{stripTabs: op == "<<-", quoted: quoted, delim: dw.value}
				pending = append(pending, hd)
				tokens = append(tokens, rawToken{kind: "op", raw: op, heredoc: hd})
				continue
			}
			if op == "<<<" {
				for i < n && (src[i] == ' ' || src[i] == '\t') {
					i++
				}
				wordRaw, afterWord := scanShellWord(src, i)
				i = afterWord
				w := parseBashWord(wordRaw)
				hs := &bashHerestring{word: w, body: w.value + "\n", literal: w.literal}
				tokens = append(tokens, rawToken{kind: "op", raw: op, herestr: hs})
				continue
			}
			tokens = append(tokens, rawToken{kind: "op", raw: op})
			continue
		}
		wordRaw, next := scanShellWord(src, i)
		if next <= i {
			i++
			continue
		}
		i = next
		tokens = append(tokens, rawToken{kind: "word", raw: wordRaw})
	}
	if len(pending) > 0 {
		consumeHeredocs()
	}
	return tokens
}

func scanOperatorToken(src string, i int) (string, int, bool) {
	rem := src[i:]
	for _, op := range []string{"<<-", "<<<", "1>>", "2>>", "&>>", "1>&", "2>&", "0<&", "&&", "||", "<<", ">>", ">|", "&>", ">&", "<&", "1>", "2>", "0<", ";", "|", "&", ">", "<"} {
		if strings.HasPrefix(rem, op) {
			return op, i + len(op), true
		}
	}
	return "", i, false
}

func scanShellWord(src string, start int) (string, int) {
	i, n := start, len(src)
	for i < n {
		c := src[i]
		if isShellSpace(c) || c == '\n' || c == ';' || c == '|' || c == '&' || c == '<' || c == '>' || c == '(' || c == ')' {
			break
		}
		if c == '\'' {
			i++
			for i < n && src[i] != '\'' {
				i++
			}
			if i < n {
				i++
			}
			continue
		}
		if c == '"' {
			i++
			for i < n && src[i] != '"' {
				if src[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				i++
			}
			if i < n {
				i++
			}
			continue
		}
		if c == '\\' && i+1 < n {
			i += 2
			continue
		}
		i++
	}
	return src[start:i], i
}

func scanBalancedGroup(src string, start int) (raw, inner string, next int, ok bool) {
	open := src[start]
	closeCh := byte(')')
	if open == '{' {
		closeCh = '}'
	}
	depth := 1
	i, n := start+1, len(src)
	for i < n && depth > 0 {
		c := src[i]
		if c == '\'' {
			i++
			for i < n && src[i] != '\'' {
				i++
			}
		} else if c == '"' {
			i++
			for i < n && src[i] != '"' {
				if src[i] == '\\' && i+1 < n {
					i++
				}
				i++
			}
		} else if c == '\\' && i+1 < n {
			i++
		} else if c == open {
			depth++
		} else if c == closeCh {
			depth--
			if depth == 0 {
				return src[start : i+1], strings.TrimSpace(src[start+1 : i]), i + 1, true
			}
		}
		i++
	}
	return "", "", start, false
}

func parseBashWord(raw string) bashWord {
	if raw == "" {
		return bashWord{}
	}
	var b strings.Builder
	literal := true
	n := len(raw)
	for i := 0; i < n; {
		c := raw[i]
		if c == '\'' {
			i++
			for i < n && raw[i] != '\'' {
				b.WriteByte(raw[i])
				i++
			}
			if i < n {
				i++
			}
			continue
		}
		if c == '"' {
			i++
			for i < n && raw[i] != '"' {
				if raw[i] == '$' || raw[i] == '`' {
					literal = false
				}
				if raw[i] == '\\' && i+1 < n {
					next := raw[i+1]
					if next == '\n' {
						i += 2
						continue
					}
					if next == '"' || next == '\\' || next == '$' || next == '`' {
						b.WriteByte(next)
						i += 2
						continue
					}
					b.WriteByte('\\')
					b.WriteByte(next)
					i += 2
					continue
				}
				b.WriteByte(raw[i])
				i++
			}
			if i < n {
				i++
			}
			continue
		}
		if c == '\\' && i+1 < n {
			b.WriteByte(raw[i+1])
			i += 2
			continue
		}
		if c == '$' || c == '`' || c == '*' || c == '?' || c == '[' || (c == '~' && i == 0) {
			literal = false
		}
		b.WriteByte(c)
		i++
	}
	return bashWord{raw: raw, value: b.String(), literal: literal}
}

func hasUnquotedShellExpansion(body string) bool {
	return strings.ContainsAny(body, "$`\\")
}

func isShellSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r'
}

func isEnvAssignment(word bashWord) bool {
	eq := strings.IndexByte(word.raw, '=')
	if eq <= 0 {
		return false
	}
	for i := 0; i < eq; i++ {
		r := rune(word.raw[i])
		if i == 0 && !unicode.IsLetter(r) && r != '_' {
			return false
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}
	return true
}
