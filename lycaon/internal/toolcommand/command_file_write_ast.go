package toolcommand

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

const bashParseTimeout = 50 * time.Millisecond

func parseBashStatements(tokens []rawToken) []bashStatement {
	var stmts []bashStatement
	var curStmt bashStatement
	var curStage bashStage

	flushStage := func() {
		if len(curStage.argv) > 0 || len(curStage.redirects) > 0 || len(curStage.heredocs) > 0 || len(curStage.herestrings) > 0 || curStage.subshell != "" {
			curStmt.stages = append(curStmt.stages, curStage)
			curStage = bashStage{}
		}
	}
	flushStmt := func(nextOp string) {
		flushStage()
		if len(curStmt.stages) > 0 {
			stmts = append(stmts, curStmt)
			curStmt = bashStatement{op: nextOp}
		} else if nextOp != "" && nextOp != "\n" {
			curStmt.op = nextOp
		}
	}

	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		switch tok.kind {
		case "group":
			curStage.subshell = tok.groupBody
		case "op":
			switch tok.raw {
			case ";", "\n", "&&", "||", "&":
				flushStmt(tok.raw)
			case "|":
				flushStage()
			case "<<", "<<-":
				if tok.heredoc != nil {
					curStage.heredocs = append(curStage.heredocs, *tok.heredoc)
				}
			case "<<<":
				if tok.herestr != nil {
					curStage.herestrings = append(curStage.herestrings, *tok.herestr)
				}
			default:
				var target bashWord
				if i+1 < len(tokens) && tokens[i+1].kind == "word" {
					i++
					target = parseBashWord(tokens[i].raw)
				}
				curStage.redirects = append(curStage.redirects, bashRedirect{op: tok.raw, target: target})
			}
		case "word":
			w := parseBashWord(tok.raw)
			if len(curStage.argv) == 0 && curStage.subshell == "" && isEnvAssignment(w) {
				curStage.env = append(curStage.env, w.raw)
				continue
			}
			curStage.argv = append(curStage.argv, w)
		}
	}
	flushStmt("")
	return stmts
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

// treeSitterBashFileWrite detects heredoc/herestring file writes inside compound
// bash constructs (such as loops or conditionals) that cannot be flattened into
// a linear command sequence.
func treeSitterBashFileWrite(ctx context.Context, src, stdoutTo string) (path string, appendMode, matched bool) {
	if !strings.Contains(src, "<<") {
		return "", false, false
	}
	entry := grammars.DetectLanguageByName("bash")
	if entry == nil {
		return "", false, false
	}
	lang := entry.Language()
	tree, err := tsparse.ParseWithin(ctx, lang, []byte(src), bashParseTimeout)
	if tree != nil {
		defer tree.Release()
	}
	if err != nil || tree == nil || tree.RootNode() == nil {
		return "", false, false
	}
	srcBytes := []byte(src)
	var walk func(n *gotreesitter.Node) bool
	walk = func(n *gotreesitter.Node) bool {
		if n == nil {
			return false
		}
		if n.Type(lang) == "redirected_statement" || n.Type(lang) == "command" || n.Type(lang) == "pipeline" {
			if p, app, ok := inspectASTNodeFileWrite(n, lang, srcBytes, stdoutTo); ok {
				path, appendMode, matched = p, app, true
				return true
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			if walk(n.Child(i)) {
				return true
			}
		}
		return false
	}
	walk(tree.RootNode())
	return path, appendMode, matched
}

func inspectASTNodeFileWrite(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, stdoutTo string) (string, bool, bool) {
	hasHeredoc := false
	var targetPath string
	var appendMode bool
	var inspect func(cur *gotreesitter.Node)
	inspect = func(cur *gotreesitter.Node) {
		if cur == nil {
			return
		}
		switch cur.Type(lang) {
		case "heredoc_redirect", "herestring_redirect":
			hasHeredoc = true
		case "file_redirect":
			text := strings.TrimSpace(string(src[cur.StartByte():cur.EndByte()]))
			if p, app, ok := parseRawOutputRedirectText(text); ok && targetPath == "" {
				targetPath, appendMode = p, app
			}
		}
		for i := 0; i < int(cur.ChildCount()); i++ {
			inspect(cur.Child(i))
		}
	}
	inspect(n)
	if !hasHeredoc {
		return "", false, false
	}
	if targetPath == "" && stdoutTo != "" && stdoutTo != "/dev/null" {
		targetPath = stdoutTo
	}
	if targetPath == "" {
		return "", false, false
	}
	return targetPath, appendMode, true
}

func parseRawOutputRedirectText(text string) (string, bool, bool) {
	for _, prefix := range []struct {
		op     string
		append bool
	}{
		{"1>>", true}, {"&>>", true}, {">>", true},
		{">|", false}, {"&>", false}, {">&", false}, {"1>", false}, {">", false},
	} {
		if strings.HasPrefix(text, prefix.op) {
			rest := strings.TrimSpace(strings.TrimPrefix(text, prefix.op))
			w := parseBashWord(rest)
			if prefix.op == ">&" && isFileDescriptorTarget(w.value) {
				return "", false, false
			}
			if isStdoutFileTarget(w.value) {
				return w.value, prefix.append, true
			}
			return "", false, false
		}
	}
	return "", false, false
}
